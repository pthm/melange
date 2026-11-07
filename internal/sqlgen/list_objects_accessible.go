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
// list_{type}_{relation}_obj function pages the same query, except for
// relations whose list is a breadth-first walk in PL/pgSQL; for those the
// accessible function selects from the list function instead.
//
// The list function embeds the query rather than calling the accessible
// function, and runs a query without the arms that only match userset
// subjects when the subject is a plain one. PL/pgSQL replans a statement on
// every call while its generic plan (built once, for any arguments) is
// estimated as costlier than the plans built for the call's arguments. The
// userset-only arms fold away from the latter for a plain subject but are
// costed in full in the former, which kept every call replanning; and each
// replan of a call to the accessible function parses and analyzes its body
// again. On small sets the planning took several times as long as the query.
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
// page of q: the IDs after the cursor, in order, up to p_limit, with
// next_cursor set to the page's last ID when more remain. A plain subject is
// paged over q.Plain, and a userset subject over the accessible function.
func renderListObjectsOverAccessible(plan ListPlan, header []string, q accessibleQuery) string {
	body := listObjectsPage(q.Full)
	if q.Split {
		accessible := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
		var plain []Stmt
		if q.Plain != "" {
			plain = []Stmt{listObjectsPage(q.Plain)}
		}
		body = If{
			Cond: HasUserset{Source: SubjectID},
			Then: []Stmt{listObjectsPage(fmt.Sprintf("SELECT * FROM %s(p_subject_type, p_subject_id)", accessible))},
			Else: plain,
		}
	}
	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  header,
		Body:    []Stmt{body},
	}
	return fn.SQL()
}

// listObjectsPage returns the page of query, which returns object_id.
func listObjectsPage(query string) Stmt {
	return ReturnQuery{Query: fmt.Sprintf(`WITH page AS (
    SELECT DISTINCT a.object_id
    FROM (
%s
    ) AS a(object_id)
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
LIMIT p_limit`, IndentLines(query, "        "))}
}

// accessibleQuery is a relation's accessible query, as the accessible function
// returns it (Full) and without the arms that only match userset subjects
// (Plain, "" when no arm matches a plain subject). Split reports whether there
// were such arms, so Plain differs from Full.
type accessibleQuery struct {
	Full, Plain string
	Split       bool
}

// accessibleQueryOf renders blocks into the accessible query with render, and
// again without their UsersetSubjectOnly blocks for plain subjects.
func accessibleQueryOf(blocks []TypedQueryBlock, render func([]TypedQueryBlock) string) accessibleQuery {
	plain, split := withoutUsersetSubjectBlocks(blocks)
	q := accessibleQuery{Full: render(blocks), Split: split}
	if !split {
		q.Plain = q.Full
	} else if len(plain) > 0 {
		q.Plain = render(plain)
	}
	return q
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
		if block.UsersetSubjectOnly && !requiresUsersetSubject(stmt.Where) {
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

// overAccessible returns the list function paging q and the accessible
// function returning it.
func overAccessible(plan ListPlan, header []string, q accessibleQuery) listObjectsWithAccessible {
	return listObjectsWithAccessible{
		List:       renderListObjectsOverAccessible(plan, header, q),
		Accessible: renderAccessibleObjectsFunction(plan, q.Full),
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
func accessibleObjectsQuery(blocks BlockSet) accessibleQuery {
	return accessibleQueryOf(blocks.Primary, unionAccessibleArms)
}

// composedAccessibleObjectsQuery returns the accessible query of a composed
// plan: its blocks and self-candidate, for userset subjects or allowed
// subject types (the type guard of the composed list function).
func composedAccessibleObjectsQuery(blocks ComposedObjectsBlockSet) accessibleQuery {
	main := blocks.MainBlocks
	if blocks.SelfBlock != nil {
		main = append(slices.Clone(main), *blocks.SelfBlock)
	}
	guard := Or(HasUserset{Source: SubjectID}, In{Expr: SubjectType, Values: blocks.AllowedSubjectTypes})
	return accessibleQueryOf(main, func(arms []TypedQueryBlock) string {
		return fmt.Sprintf("SELECT g.object_id\nFROM (\n%s\n) AS g\nWHERE %s",
			IndentLines(unionAccessibleArms(arms), "    "), guard.SQL())
	})
}

// recursiveAccessibleObjectsQuery returns the accessible query of a recursive
// plan without a self-referential walk: the union of its blocks, after the
// hoisted CTEs, filtered as the accessible set (accessibleObjectsSelect), and
// the self-candidate, which the filters do not apply to.
func recursiveAccessibleObjectsQuery(plan ListPlan, blocks RecursiveBlockSet) accessibleQuery {
	render := func(base []TypedQueryBlock, self *TypedQueryBlock) string {
		var ctes []CTEDef
		var arms []string
		if len(base) > 0 {
			ctes = append(slices.Clone(blocks.HoistedCTEs),
				CTEDef{Name: "accessible", Columns: []string{"object_id"}, Query: Raw(unionAccessibleArms(base))})
			filtered := accessibleObjectsSelect(plan, blocks, TableAs("", "accessible", "acc"))
			filtered.Distinct = false
			arms = append(arms, filtered.SQL())
		}
		if self != nil {
			arms = append(arms, self.Query.SQL())
		}
		return WithCTE{CTEs: ctes, Query: Raw(joinUnionAllBlocksSQL(arms))}.SQL()
	}
	q := accessibleQueryOf(blocks.BaseBlocks, func(base []TypedQueryBlock) string { return render(base, nil) })
	// The self-candidate only matches userset subjects: it joins the full query,
	// which plain subjects then no longer share.
	if self := blocks.SelfCandidateBlock; self != nil {
		q.Full = render(blocks.BaseBlocks, self)
		q.Split = true
	}
	return q
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
