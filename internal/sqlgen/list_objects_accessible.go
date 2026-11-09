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
// The function takes the subject kind as an optional third argument
// (p_userset, whether the subject is a userset), which applications leave out
// and generated callers pass as a literal. Its arms that only match one kind
// test the argument as well as the subject, and the functions it composes are
// called with it. A caller passing a literal kind folds the other kind's arms
// away at plan time, even in a generic plan (one built once, for any
// arguments), so the list function can keep its plan. With only the subject's
// position test, a generic plan kept every userset arm and its per-row checks
// in every composed function, costed in full, so PL/pgSQL planned each call;
// on a view over many tables that planning took tens of milliseconds for a
// composed relation whose query ran in under one. A kind that contradicts the
// subject returns no rows, since a kind test can sit under a negation (an
// exclusion) where trusting it would grant them.
//
// For a plain subject the list function embeds the query without its
// userset-only arms, with the subject kind bound to FALSE; for a userset
// subject it pages the accessible function called with TRUE. Leaving the arms
// out of the text, rather than folding them, matters too: PostgreSQL does not
// plan a parallel Append over a UNION ALL with an arm it has proven empty.
//
// accessible_objects(p_subject_type, p_subject_id, p_relation, p_object_type)
// takes the relation and type as arguments. It selects from the
// list_accessible_objects dispatcher, so it is not inlined: it plans in
// constant time however large the model is (an inlined union over every
// relation would be planned in full on each query), and is as fast as the
// per-relation function for small sets, slower for large ones.
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

// subjectKindParam is the accessible function's optional subject kind
// argument: whether the subject is a userset. In a LANGUAGE sql body a column
// of the same name in melange_tuples would win over it, as over p_subject_id;
// the p_ prefix, shared with the other arguments, makes that unlikely.
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
// replaced by a literal kind, TRUE or FALSE, as in a list function.
func bindSubjectKind(query, kind string) string {
	negated := "TRUE"
	if kind == "TRUE" {
		negated = "FALSE"
	}
	return strings.NewReplacer(subjectIsUserset, kind, subjectNotUserset, negated, subjectKindToken, kind).Replace(query)
}

// bindSubjectKindParam returns query with the subject kind bound to the
// accessible function's optional argument (subjectKindParam), or to the
// subject when it is omitted, and only the rows of a call whose argument
// agrees with the subject. A literal argument folds the kind tests to
// constants at plan time, as bindSubjectKind does, so the other kind's arms
// drop out of a generic plan. The guard is what makes the argument safe to
// expose: a kind test can sit under a negation (an exclusion's NOT EXISTS),
// where trusting a wrong kind would grant rows, so a contradicting call
// returns nothing instead. Composed calls pass the argument on.
func bindSubjectKindParam(query string) string {
	kind := "COALESCE(" + subjectKindParam + ", " + subjectIsUserset + ")"
	bound := strings.NewReplacer(
		subjectIsUserset, kind,
		subjectNotUserset, "NOT "+kind,
		subjectKindToken, subjectKindParam,
	).Replace(query)
	return fmt.Sprintf("SELECT a.object_id\nFROM (\n%s\n) AS a(object_id)\nWHERE %s IS NULL OR %s = (%s)",
		IndentLines(bound, "    "), subjectKindParam, subjectKindParam, subjectIsUserset)
}

// renderAccessibleObjectsFunction renders the accessible function, returning
// the object_id column of query. Applications call it with the subject; list
// functions also pass the subject kind as a literal, so a generic plan of
// theirs drops the arms that only match the other kind.
func renderAccessibleObjectsFunction(plan ListPlan, query string) string {
	name := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
	return fmt.Sprintf(`-- Objects of type %s on which the subject holds %s, for filtering queries:
-- WHERE id IN (SELECT * FROM %s(subject_type, subject_id)).
-- LANGUAGE sql without SET options, so the planner inlines it. %s, which
-- generated callers pass, says whether the subject is a userset; omitted,
-- it is read from the subject, and a value that contradicts it returns no rows.
CREATE OR REPLACE FUNCTION %s(
    p_subject_type TEXT,
    p_subject_id TEXT,
    %s BOOLEAN DEFAULT NULL
) RETURNS TABLE(object_id TEXT) AS $$
%s
$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;`,
		plan.ObjectType, plan.Relation, name, subjectKindParam, name, subjectKindParam, bindSubjectKindParam(query))
}

// renderListObjectsOverAccessible renders list_{type}_{relation}_obj as a
// page of q: the IDs after the cursor, in order, up to p_limit, with
// next_cursor set to the page's last ID when more remain. A plain subject is
// paged over q.Plain with the subject kind bound to FALSE; a userset subject,
// when its query differs, over the internal accessible function with TRUE.
func renderListObjectsOverAccessible(plan ListPlan, header []string, q accessibleQuery) string {
	plain := bindSubjectKind(q.Plain, "FALSE")
	plainPaged := bindSubjectKind(q.PlainPaged, "FALSE")
	var body Stmt
	if q.Plain != "" && plain == bindSubjectKind(q.Full, "TRUE") {
		// Both kinds bind to the same query, so there is nothing to branch on.
		body = listObjectsPage(plain, plainPaged)
	} else {
		accessible := sqldsl.PrefixIdent(AccessibleObjectsFunctionName(plan.ObjectType, plan.Relation), plan.DatabaseSchema)
		byKind := If{
			Cond: HasUserset{Source: SubjectID},
			Then: []Stmt{listObjectsPage(fmt.Sprintf("SELECT k.object_id FROM %s(p_subject_type, p_subject_id, TRUE) AS k", accessible), "")},
		}
		if q.Plain != "" {
			byKind.Else = []Stmt{listObjectsPage(plain, plainPaged)}
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
// The paged statement reads paged instead when it is set: the same objects,
// with arms that already keep only a page of each parent (pagedAccessibleArms).
func listObjectsPage(query, paged string) Stmt {
	if paged == "" {
		paged = query
	}
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
LIMIT p_limit`, IndentLines(paged, "        "))
	return If{
		Cond: Raw("p_limit IS NULL"),
		Then: []Stmt{ReturnQuery{Query: all}},
		Else: []Stmt{ReturnQuery{Query: page}},
	}
}

// accessibleQuery is a relation's accessible query, as the accessible function
// returns it (Full) and without the arms that only match userset subjects
// (Plain, "" when no arm matches a plain subject). PlainPaged is Plain for a
// list page, with its parent lookups reading a page of each parent
// (pagedAccessibleArms), or "" when it has none.
type accessibleQuery struct {
	Full, Plain, PlainPaged string
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
		arms = append(arms, accessibleArm(block))
	}
	return joinUnionAllBlocksSQL(arms)
}

// accessibleArm renders block as an arm of unionAccessibleArms.
func accessibleArm(block TypedQueryBlock) string {
	stmt := block.Query
	stmt.Distinct = false
	if block.UsersetSubjectOnly && !requiresUsersetSubject(stmt.Where) {
		guard := Expr(HasUserset{Source: SubjectID})
		if stmt.Where != nil {
			guard = And(guard, stmt.Where)
		}
		stmt.Where = guard
	}
	return formatQueryBlockSQL(block.Comments, stmt.SQL())
}

// pagedAccessibleArms is unionAccessibleArms for a list page: a block read by
// parent (ByParent) keeps the first p_limit + 1 objects after the cursor
// under each parent, merged across parents, instead of every object of every
// parent. Every object of the page is among its parent's first p_limit + 1,
// so the page over these arms is the page over the full ones; with an index
// on the link and object ID, a parent costs a page of index entries however
// many objects it has.
func pagedAccessibleArms(blocks []TypedQueryBlock) string {
	arms := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.ByParent == nil {
			arms = append(arms, accessibleArm(block))
			continue
		}
		by := block.ByParent
		lookup := by.Lookup
		lookup.Where = And(lookup.Where, Gt{Left: by.Object, Right: Raw("COALESCE(p_after, '')")})
		arm := fmt.Sprintf(`SELECT x.object_id
FROM %s AS par,
LATERAL (
%s
    ORDER BY %s
    LIMIT p_limit + 1
) AS x`, by.Parents, IndentLines(lookup.SQL(), "    "), by.Object.SQL())
		comments := slices.Clone(block.Comments)
		if n := len(comments); n > 0 {
			comments[n-1] += ", a page of each parent"
		}
		arms = append(arms, formatQueryBlockSQL(comments, arm))
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
// selects its whole list. The list function has no subject kind argument, so
// the accessible functions it composes get the kind test itself.
func overList(plan ListPlan, list string) listObjectsWithAccessible {
	query := fmt.Sprintf("SELECT o.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) AS o",
		sqldsl.PrefixIdent(plan.FunctionName, plan.DatabaseSchema))
	return listObjectsWithAccessible{
		List:       strings.ReplaceAll(list, subjectKindToken, subjectIsUserset),
		Accessible: renderAccessibleObjectsFunction(plan, query),
	}
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
	render := func(union func([]TypedQueryBlock) string) func([]TypedQueryBlock) string {
		return func(arms []TypedQueryBlock) string {
			return fmt.Sprintf("SELECT g.object_id\nFROM (\n%s\n) AS g\nWHERE %s",
				IndentLines(union(arms), "    "), guard.SQL())
		}
	}
	q := accessibleQueryOf(main, render(unionAccessibleArms))
	if plain, _ := withoutUsersetSubjectBlocks(main); hasParentLookup(plain) {
		q.PlainPaged = render(pagedAccessibleArms)(plain)
	}
	return q
}

// recursiveAccessibleObjectsQuery returns the accessible query of a recursive
// plan without a self-referential walk: the union of its blocks, after the
// hoisted CTEs, filtered as the accessible set (accessibleObjectsSelect), and
// the self-candidate, which the filters do not apply to.
func recursiveAccessibleObjectsQuery(plan ListPlan, blocks RecursiveBlockSet) accessibleQuery {
	filtered := accessibleObjectsSelect(plan, blocks, TableAs("", "accessible", "acc"))
	filtered.Distinct = false
	render := func(base []TypedQueryBlock, self *TypedQueryBlock, union func([]TypedQueryBlock) string) string {
		var ctes []CTEDef
		var arms []string
		if len(base) > 0 {
			ctes = append(slices.Clone(blocks.HoistedCTEs),
				CTEDef{Name: "accessible", Columns: []string{"object_id"}, Query: Raw(union(base))})
			arms = append(arms, filtered.SQL())
		}
		if self != nil {
			arms = append(arms, self.Query.SQL())
		}
		return WithCTE{CTEs: ctes, Query: Raw(joinUnionAllBlocksSQL(arms))}.SQL()
	}
	q := accessibleQueryOf(blocks.BaseBlocks, func(base []TypedQueryBlock) string { return render(base, nil, unionAccessibleArms) })
	// The self-candidate only matches userset subjects: it joins the full query
	// but not the plain one.
	if self := blocks.SelfCandidateBlock; self != nil {
		q.Full = render(blocks.BaseBlocks, self, unionAccessibleArms)
	}
	// A page of each parent is a page of the accessible set only when nothing
	// filters the set afterwards: an exclusion or check over it could drop a
	// parent's first objects and need the ones its page left out.
	if plain, _ := withoutUsersetSubjectBlocks(blocks.BaseBlocks); filtered.Where == nil && hasParentLookup(plain) {
		q.PlainPaged = render(plain, nil, pagedAccessibleArms)
	}
	return q
}

// hasParentLookup reports whether any of blocks can be read by parent.
func hasParentLookup(blocks []TypedQueryBlock) bool {
	return slices.ContainsFunc(blocks, func(b TypedQueryBlock) bool { return b.ByParent != nil })
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

// accessibleObjectsCall calls the accessible function of (objectType,
// relation) for the query subject and its kind, as alias.
func accessibleObjectsCall(schema, objectType, relation, alias string) FunctionCallExpr {
	return FunctionCallExpr{
		Schema: schema,
		Name:   AccessibleObjectsFunctionName(objectType, relation),
		Args:   []Expr{SubjectType, SubjectID, Raw(subjectKindToken)},
		Alias:  alias,
	}
}
