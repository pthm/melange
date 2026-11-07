package sqlgen

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// accessible_{type}_{relation}(p_subject_type, p_subject_id) returns the IDs of
// the objects of type on which the subject holds relation. It is a single SQL
// query in a LANGUAGE sql function without a SET clause, so PostgreSQL inlines
// it into the calling query: an application filters its own query with
//
//	WHERE id IN (SELECT * FROM accessible_document_viewer('user', $1))
//
// and the planner combines the access set with the application's filters,
// ordering and limit instead of evaluating an opaque function. The paginated
// list_{type}_{relation}_obj function is a wrapper over it, except for
// relations whose list is a breadth-first walk in PL/pgSQL; for those the
// accessible function selects from the list function instead.
//
// Like every generated function body, it names melange_tuples unqualified:
// contextual tuples shadow the table with a temporary view, which unqualified
// lookups find first. An inlined body resolves the name in the caller's
// search_path, so the calling query must have melange_tuples on it.

// AccessibleObjectsFunctionName returns the name of the accessible-objects
// function for (objectType, relation).
func AccessibleObjectsFunctionName(objectType, relation string) string {
	return SafeIdentifier("accessible_", objectType, relation, "")
}

// renderAccessibleObjectsFunction renders the accessible function returning
// the object_id column of query.
func renderAccessibleObjectsFunction(plan ListPlan, query string) string {
	name := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
	return fmt.Sprintf(`-- Objects of type %s on which the subject holds %s, for filtering queries:
-- WHERE id IN (SELECT * FROM %s(subject_type, subject_id)).
-- LANGUAGE sql without SET options, so the planner inlines it.
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
%s
$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;`,
		plan.ObjectType, plan.Relation, name, name, query)
}

// renderListObjectsOverAccessible renders list_{type}_{relation}_obj as a
// page of the accessible function: the IDs after the cursor, in order, up to
// p_limit, with next_cursor set to the page's last ID when more remain.
func renderListObjectsOverAccessible(plan ListPlan, header []string) string {
	accessible := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
	page := fmt.Sprintf(`WITH page AS (
    SELECT DISTINCT a.object_id
    FROM %s(p_subject_type, p_subject_id) AS a(object_id)
    WHERE a.object_id > COALESCE(p_after, '')
    ORDER BY a.object_id
    LIMIT p_limit + 1
)
SELECT p.object_id,
    CASE WHEN p_limit > 0 AND (SELECT count(*) FROM page) > p_limit
        THEN (SELECT x.object_id FROM page x ORDER BY x.object_id OFFSET p_limit - 1 LIMIT 1)
    END AS next_cursor
FROM page p
ORDER BY p.object_id
LIMIT p_limit`, accessible)
	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  header,
		Body:    []Stmt{ReturnQuery{Query: page}},
	}
	return fn.SQL()
}

// unionAccessibleArms joins blocks, each a query returning object_id, with
// UNION ALL. Arms that only match userset query subjects are guarded on the
// subject kind, which the PL/pgSQL list functions branch on instead.
//
// Nothing is deduplicated: each arm's DISTINCT is dropped. A caller filtering
// by the set (IN, a semi-join) does not need distinct rows, and the list
// function deduplicates once while sorting its page; deduplicating at each
// level would hash the whole set several times and keep the planner from
// merging the arms in order and stopping at the page.
func unionAccessibleArms(blocks []TypedQueryBlock) string {
	arms := make([]string, 0, len(blocks))
	for _, block := range blocks {
		stmt := block.Query
		stmt.Distinct = false
		if block.UsersetSubjectOnly {
			guard := Expr(HasUserset{Source: SubjectID})
			if stmt.Where != nil {
				guard = And(guard, stmt.Where)
			}
			stmt.Where = guard
		}
		arms = append(arms, formatQueryBlockSQL(block.Comments, stmt.SQL()))
	}
	return joinUnionAllBlocksSQL(arms)
}

// listObjectsWithAccessible is the SQL generated for one relation's
// list_objects: the paginated list function and the accessible function.
type listObjectsWithAccessible struct {
	List, Accessible string
}

// overAccessible returns the list function as a page of query's accessible
// function.
func overAccessible(plan ListPlan, header []string, query string) listObjectsWithAccessible {
	return listObjectsWithAccessible{
		List:       renderListObjectsOverAccessible(plan, header),
		Accessible: renderAccessibleObjectsFunction(plan, query),
	}
}

// overList returns a PL/pgSQL list function with an accessible function that
// selects its whole list.
func overList(plan ListPlan, list string) listObjectsWithAccessible {
	query := fmt.Sprintf("SELECT o.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) AS o",
		sqldsl.PrefixIdent(plan.FunctionName, plan.DatabaseSchema))
	return listObjectsWithAccessible{List: list, Accessible: renderAccessibleObjectsFunction(plan, query)}
}

// accessibleObjectsQuery returns the accessible query of a standard (direct,
// userset or intersection) plan: the union of its blocks.
func accessibleObjectsQuery(blocks BlockSet) string {
	return unionAccessibleArms(blocks.Primary)
}

// composedAccessibleObjectsQuery returns the accessible query of a composed
// plan: its blocks and self-candidate, for userset subjects or allowed
// subject types (the type guard of the composed list function).
func composedAccessibleObjectsQuery(blocks ComposedObjectsBlockSet) string {
	main := blocks.MainBlocks
	if blocks.SelfBlock != nil {
		main = append(slices.Clone(main), *blocks.SelfBlock)
	}
	guard := Or(HasUserset{Source: SubjectID}, In{Expr: SubjectType, Values: blocks.AllowedSubjectTypes})
	return fmt.Sprintf("SELECT g.object_id\nFROM (\n%s\n) AS g\nWHERE %s",
		IndentLines(unionAccessibleArms(main), "    "), guard.SQL())
}

// recursiveAccessibleObjectsQuery returns the accessible query of a recursive
// plan without a self-referential walk: the union of its blocks, after the
// hoisted CTEs, filtered as the accessible set (accessibleObjectsSelect), and
// the self-candidate, which the filters do not apply to.
func recursiveAccessibleObjectsQuery(plan ListPlan, blocks RecursiveBlockSet) string {
	ctes := append(slices.Clone(blocks.HoistedCTEs),
		CTEDef{Name: "accessible", Columns: []string{"object_id"}, Query: Raw(unionAccessibleArms(blocks.BaseBlocks))})
	filtered := accessibleObjectsSelect(plan, blocks, TableAs("", "accessible", "acc"))
	filtered.Distinct = false
	arms := []string{filtered.SQL()}
	if blocks.SelfCandidateBlock != nil {
		arms = append(arms, blocks.SelfCandidateBlock.Query.SQL())
	}
	return WithCTE{CTEs: ctes, Query: Raw(joinUnionAllBlocksSQL(arms))}.SQL()
}

// accessibleCallRef matches a call of an accessible function in a function body.
var accessibleCallRef = regexp.MustCompile(`accessible_[A-Za-z0-9_]+`)

// orderAccessibleFunctions orders fns so each comes after the accessible
// functions its body calls. Calls follow composition, which is acyclic
// (listCompositionSafe); a cycle would be left in input order.
func orderAccessibleFunctions(fns []NamedFunction) []NamedFunction {
	byName := make(map[string]NamedFunction, len(fns))
	for _, fn := range fns {
		byName[fn.Name] = fn
	}
	ordered := make([]NamedFunction, 0, len(fns))
	visited := make(map[string]bool, len(fns))
	var visit func(fn NamedFunction)
	visit = func(fn NamedFunction) {
		if visited[fn.Name] {
			return
		}
		// Marked before its dependencies, so self-calls and cycles stop here.
		visited[fn.Name] = true
		_, body, _ := strings.Cut(fn.SQL, "AS $$")
		for _, name := range accessibleCallRef.FindAllString(body, -1) {
			if dep, ok := byName[name]; ok {
				visit(dep)
			}
		}
		ordered = append(ordered, fn)
	}
	for _, fn := range fns {
		visit(fn)
	}
	return ordered
}

// accessibleObjectsCall calls the accessible function of (objectType, relation)
// for the query subject, as alias.
func accessibleObjectsCall(schema, objectType, relation, alias string) FunctionCallExpr {
	return FunctionCallExpr{
		Schema: schema,
		Name:   AccessibleObjectsFunctionName(objectType, relation),
		Args:   []Expr{SubjectType, SubjectID},
		Alias:  alias,
	}
}
