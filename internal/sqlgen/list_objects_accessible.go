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
// Each accessible function has an internal form, accessiblesk_{type}_{relation},
// which takes the subject kind as a third argument (p_userset, whether the
// subject is a userset). Its arms that only match userset subjects test
// p_userset, and the functions it composes are called with it; the public
// function passes position('#' in p_subject_id) > 0. A caller passing a
// literal kind folds those arms away at plan time, even in a generic plan
// (one built once, for any arguments), so the list function can keep its
// plan. With the position test instead, a generic plan kept every userset arm
// and its per-row checks in every composed function, costed in full, so
// PL/pgSQL planned each call; on a view over many tables that planning took
// tens of milliseconds for a composed relation whose query ran in under one.
//
// For a plain subject the list function embeds the query without its
// userset-only arms, with the subject kind bound to FALSE; for a userset
// subject it pages the internal accessible function called with TRUE. Leaving
// the arms out of the text, rather than folding them, matters too: PostgreSQL
// does not plan a parallel Append over a UNION ALL with an arm it has proven
// empty.
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

// accessibleSubjectKindFunctionName returns the name of the internal form of
// the accessible function, which takes the subject kind. Its prefix is its
// own, so no relation name can make it another generated function's name.
func accessibleSubjectKindFunctionName(objectType, relation string) string {
	return SafeIdentifier("accessiblesk_", objectType, relation, "")
}

// subjectKindParam is the internal accessible function's subject kind
// argument: whether the subject is a userset.
const subjectKindParam = "p_userset"

// subjectKindToken stands for the subject kind in a composed call until the
// query is bound (bindSubjectKind). Type and relation names cannot contain
// '#', so neither the token nor the kind tests below occur in a model name.
const subjectKindToken = "#subject_kind#"

var (
	subjectIsUserset  = HasUserset{Source: SubjectID}.SQL()
	subjectNotUserset = NoUserset{Source: SubjectID}.SQL()
)

// bindSubjectKind returns query with the subject kind tests and tokens
// replaced by kind: TRUE or FALSE in a list function, or subjectKindParam in
// the internal accessible function.
func bindSubjectKind(query, kind string) string {
	negated := "(NOT " + kind + ")"
	switch kind {
	case "TRUE":
		negated = "FALSE"
	case "FALSE":
		negated = "TRUE"
	}
	return strings.NewReplacer(subjectIsUserset, kind, subjectNotUserset, negated, subjectKindToken, kind).Replace(query)
}

// renderAccessibleObjectsFunctions renders the public accessible function and
// its internal form, both returning the object_id column of query.
func renderAccessibleObjectsFunctions(plan ListPlan, query string) (public, internal string) {
	name := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
	kindName := sqldsl.PrefixIdent(accessibleSubjectKindFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
	public = fmt.Sprintf(`-- Objects of type %s on which the subject holds %s, for filtering queries:
-- WHERE id IN (SELECT * FROM %s(subject_type, subject_id)).
-- LANGUAGE sql without SET options, so the planner inlines it.
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
    SELECT k.object_id FROM %s(p_subject_type, p_subject_id, %s) AS k
$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;`,
		plan.ObjectType, plan.Relation, name, name, kindName, subjectIsUserset)
	internal = fmt.Sprintf(`-- Internal form of %s: %s says whether the subject is a
-- userset, so callers passing a literal fold the arms for the other kind.
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    %s BOOLEAN
) RETURNS TABLE(object_id TEXT) AS $$
%s
$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;`,
		name, subjectKindParam, kindName, subjectKindParam, bindSubjectKind(query, subjectKindParam))
	return public, internal
}

// renderListObjectsOverAccessible renders list_{type}_{relation}_obj as a
// page of q: the IDs after the cursor, in order, up to p_limit, with
// next_cursor set to the page's last ID when more remain. A plain subject is
// paged over q.Plain with the subject kind bound to FALSE; a userset subject,
// when its query differs, over the internal accessible function with TRUE.
func renderListObjectsOverAccessible(plan ListPlan, header []string, q accessibleQuery) string {
	plain := bindSubjectKind(q.Plain, "FALSE")
	var body Stmt
	if q.Plain != "" && plain == bindSubjectKind(q.Full, "TRUE") {
		// Both kinds bind to the same query, so there is nothing to branch on.
		body = listObjectsPage(plain)
	} else {
		internal := sqldsl.PrefixIdent(accessibleSubjectKindFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
		byKind := If{
			Cond: HasUserset{Source: SubjectID},
			Then: []Stmt{listObjectsPage(fmt.Sprintf("SELECT k.object_id FROM %s(p_subject_type, p_subject_id, TRUE) AS k", internal))},
		}
		if q.Plain != "" {
			byKind.Else = []Stmt{listObjectsPage(plain)}
		}
		body = byKind
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

// listObjectsPage returns the page of query, which returns object_id. Without
// p_limit the whole set is returned by a statement of its own: a generic plan
// of the paged statement assumes a short page and walks an index in object_id
// order to stop early, which reads the whole index when there is no limit.
func listObjectsPage(query string) Stmt {
	all := fmt.Sprintf(`SELECT DISTINCT a.object_id, NULL::TEXT AS next_cursor
FROM (
%s
) AS a(object_id)
WHERE a.object_id > COALESCE(p_after, '')
ORDER BY a.object_id`, IndentLines(query, "    "))
	page := fmt.Sprintf(`WITH page AS (
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
LIMIT p_limit`, IndentLines(query, "        "))
	return If{
		Cond: Raw("p_limit IS NULL"),
		Then: []Stmt{ReturnQuery{Query: all}},
		Else: []Stmt{ReturnQuery{Query: page}},
	}
}

// accessibleQuery is a relation's accessible query, as the accessible function
// returns it (Full) and without the arms that only match userset subjects
// (Plain, "" when no arm matches a plain subject).
type accessibleQuery struct {
	Full, Plain string
}

// accessibleQueryOf renders blocks into the accessible query with render, and
// again without their userset-only blocks for plain subjects.
func accessibleQueryOf(blocks []TypedQueryBlock, render func([]TypedQueryBlock) string) accessibleQuery {
	q := accessibleQuery{Full: render(blocks)}
	if plain, split := withoutUsersetSubjectBlocks(blocks); !split {
		q.Plain = q.Full
	} else if len(plain) > 0 {
		q.Plain = render(plain)
	}
	return q
}

// unionAccessibleArms joins blocks, each a query returning object_id, with
// UNION ALL. Arms that only match userset query subjects are guarded on the
// subject kind test, which bindSubjectKind replaces with the bound kind.
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
// list_objects: the paginated list function, the accessible function and its
// internal form.
type listObjectsWithAccessible struct {
	List, Accessible, AccessibleKind string
}

// overAccessible returns the list function paging q and the accessible
// functions returning it.
func overAccessible(plan ListPlan, header []string, q accessibleQuery) listObjectsWithAccessible {
	public, internal := renderAccessibleObjectsFunctions(plan, q.Full)
	return listObjectsWithAccessible{
		List:           renderListObjectsOverAccessible(plan, header, q),
		Accessible:     public,
		AccessibleKind: internal,
	}
}

// overList returns a PL/pgSQL list function with accessible functions that
// select its whole list. The list function has no subject kind argument, so
// the internal accessible functions it composes get the kind test itself.
func overList(plan ListPlan, list string) listObjectsWithAccessible {
	query := fmt.Sprintf("SELECT o.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) AS o",
		sqldsl.PrefixIdent(plan.FunctionName, plan.DatabaseSchema))
	public, internal := renderAccessibleObjectsFunctions(plan, query)
	return listObjectsWithAccessible{List: strings.ReplaceAll(list, subjectKindToken, subjectIsUserset), Accessible: public, AccessibleKind: internal}
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
	// The self-candidate only matches userset subjects: it joins the full query
	// but not the plain one.
	if self := blocks.SelfCandidateBlock; self != nil {
		q.Full = render(blocks.BaseBlocks, self)
	}
	return q
}

// accessibleCallRef matches a call of an accessible function in a function body.
var accessibleCallRef = regexp.MustCompile(`accessible(?:sk)?_[A-Za-z0-9_]+`)

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

// accessibleObjectsCall calls the internal accessible function of
// (objectType, relation) for the query subject and its kind, as alias.
func accessibleObjectsCall(schema, objectType, relation, alias string) FunctionCallExpr {
	return FunctionCallExpr{
		Schema: schema,
		Name:   accessibleSubjectKindFunctionName(objectType, relation),
		Args:   []Expr{SubjectType, SubjectID, Raw(subjectKindToken)},
		Alias:  alias,
	}
}
