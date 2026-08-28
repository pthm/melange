package sqlgen

import (
	"strings"

	"github.com/pthm/melange/internal/sqlgen/plpgsql"
)

// Object filtering narrows list_objects results to objects that hold a given
// direct relation to a given subject — "elements the user can view, but only
// within workspace:7". It is a Melange extension: OpenFGA's ListObjects has no
// object-side filter (openfga/openfga#301, declined 2024-02-26).
//
// The filter travels as a single TEXT parameter in FGA tuple notation minus the
// object side, which p_object_type already supplies:
//
//	'workspace@workspace:7'  =>  relation 'workspace', subject workspace:7
//
// One parameter rather than three keeps the public SQL surface flat, mirroring
// how list_subjects folds its userset filter into p_subject_type ('team#member')
// and parses it into locals. The parse happens once per call in the DECLARE
// block; every predicate then reads plain local variables.
//
// Only direct relations are filterable. A filter naming a computed or
// TTU-derived relation would need a filtered expansion before the top-level
// query could resolve — the cost objection that sank the same feature upstream
// — so a userset subject is rejected rather than silently mis-scoped.
const (
	filterRelationVar    = "v_filter_relation"
	filterSubjectVar     = "v_filter_subject"
	filterSubjectTypeVar = "v_filter_subject_type"
	filterSubjectIDVar   = "v_filter_subject_id"
)

// objectFilterDecls returns the DECLARE entries that split p_filter into its
// three parts. Declarations initialise in order, so later entries may reference
// earlier ones. With p_filter NULL every split_part yields NULL, which is what
// objectFilterPredicate's short-circuit expects.
func objectFilterDecls() []plpgsql.Decl {
	return []plpgsql.Decl{
		{Name: filterRelationVar, Type: "TEXT := split_part(p_filter, '@', 1)"},
		{Name: filterSubjectVar, Type: "TEXT := substring(p_filter from position('@' in p_filter) + 1)"},
		// Split the subject on its FIRST colon only: an ID may legitimately
		// contain colons, so split_part would truncate "user:a:b" to "a".
		{Name: filterSubjectTypeVar, Type: "TEXT := split_part(" + filterSubjectVar + ", ':', 1)"},
		{Name: filterSubjectIDVar, Type: "TEXT := substring(" + filterSubjectVar + " from position(':' in " + filterSubjectVar + ") + 1)"},
	}
}

// objectFilterGuard returns the prelude statement rejecting a filter this
// function cannot honour. A filter that parsed to garbage, or that names a
// relation carrying no plain-subject tuples, would match nothing and return an
// empty list — indistinguishable from "you have access to nothing". For a
// scoping mechanism that is the wrong failure: a typo should be loud.
func objectFilterGuard(plan ListPlan) plpgsql.Stmt {
	checks := []string{
		"position('@' in p_filter) = 0",
		"OR position(':' in " + filterSubjectVar + ") = 0",
		"OR " + filterRelationVar + " = ''",
		"OR " + filterSubjectTypeVar + " = ''",
		"OR " + filterSubjectIDVar + " = ''",
		"OR position('#' in " + filterSubjectIDVar + ") > 0",
	}
	if len(plan.FilterableRelations) > 0 {
		checks = append(checks,
			"OR "+filterRelationVar+" NOT IN ("+formatSQLStringList(plan.FilterableRelations)+")")
	}

	return plpgsql.RawStmt{SQLText: `IF p_filter IS NOT NULL AND (
    ` + strings.Join(checks, "\n    ") + `
) THEN
    RAISE EXCEPTION 'melange: invalid object filter %, expected "relation@subject_type:subject_id" naming a relation directly assignable on this object type', p_filter
        USING ERRCODE = '22023';
END IF;`}
}

// objectFilterPrelude returns the declarations and guard every generated
// list_objects function needs before its query body.
func objectFilterPrelude(plan ListPlan) ([]plpgsql.Decl, []plpgsql.Stmt) {
	return objectFilterDecls(), []plpgsql.Stmt{
		Comment{Text: `Object filter: "relation@subject_type:subject_id" (NULL = no filter)`},
		objectFilterGuard(plan),
	}
}

// newListObjectsFunction assembles a list_objects function with the object
// filter wired in: the p_filter argument, the DECLARE entries that parse it,
// and the guard that rejects one this function cannot honour, with body
// appended after.
//
// Every list_objects renderer goes through here so the three cannot drift
// apart. A renderer taking ListObjectsArgs() (and so accepting p_filter) while
// forgetting the prelude would parse nothing and silently ignore the filter;
// routing them all through one constructor makes that unrepresentable.
func newListObjectsFunction(plan ListPlan, header []string, body ...Stmt) PlpgsqlFunction {
	decls, prelude := objectFilterPrelude(plan)
	return PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  header,
		Decls:   decls,
		Body:    append(prelude, body...),
	}
}

// objectFilterPredicate returns the predicate restricting idExpr to objects
// holding the filter relation to the filter subject. It is a no-op when
// p_filter is NULL, so a generated function's unfiltered plan is unchanged
// beyond one constant-folded OR.
//
// The right-hand set is an uncorrelated IN subquery rather than a correlated
// EXISTS: it leaves the planner free to drive from whichever side is smaller,
// which on a filtered call is the filter set.
func objectFilterPredicate(plan ListPlan, idExpr Expr) Expr {
	filterSet := Tuples(plan.DatabaseSchema, "flt").
		ObjectType(plan.ObjectType).
		SelectCol("object_id").
		Where(
			Eq{Left: Col{Table: "flt", Column: "relation"}, Right: ParamRef(filterRelationVar)},
			Eq{Left: Col{Table: "flt", Column: "subject_type"}, Right: ParamRef(filterSubjectTypeVar)},
			Eq{Left: Col{Table: "flt", Column: "subject_id"}, Right: ParamRef(filterSubjectIDVar)},
		).
		Build()

	return Or(
		Raw("p_filter IS NULL"),
		InSelect{Expr: idExpr, Query: filterSet},
	)
}

// filterPartQuery ANDs the object filter onto a query using whatever it
// projects as its object id, reporting whether it could.
//
// Used for INTERSECT parts: filter(A INTERSECT B) equals
// filter(A) INTERSECT filter(B), so constraining every part is sound and bounds
// each input before the set operation, where the cost actually is.
//
// The caller must not claim coverage unless every part reported true. A part
// projecting something other than a single object id cannot take the filter,
// and treating that as covered would drop the filter silently.
func filterPartQuery(plan ListPlan, stmt SelectStmt) (SelectStmt, bool) {
	idExpr := stmt.SoleColumn()
	if idExpr == nil {
		return stmt, false
	}
	// And() drops nil operands and renders a single-element AND bare, so this
	// is correct whether or not the statement already had a WHERE.
	stmt.Where = And(stmt.Where, objectFilterPredicate(plan, idExpr))
	return stmt, true
}

// applyObjectFilter pushes the filter predicate into every block that declared
// an object-id expression for it. Blocks leaving FilterIDExpr nil are not
// pushdown-eligible and rely on the pagination wrapper's post-filter instead —
// correct either way, but only the pushed-down arms avoid enumerating the
// unfiltered set.
//
// Reports whether every block took the filter inline. A false result means the
// caller must keep the pagination wrapper's post-filter to stay correct.
func applyObjectFilter(plan ListPlan, blocks []TypedQueryBlock) bool {
	pushedDown := true
	for i := range blocks {
		if blocks[i].FilterApplied {
			continue
		}
		if blocks[i].FilterIDExpr == nil {
			pushedDown = false
			continue
		}
		blocks[i].Query.Where = And(blocks[i].Query.Where, objectFilterPredicate(plan, blocks[i].FilterIDExpr))
		// Mark applied so a second pass is a no-op rather than ANDing twice.
		blocks[i].FilterApplied = true
	}
	return pushedDown
}
