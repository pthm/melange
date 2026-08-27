package sqlgen

import "github.com/pthm/melange/internal/sqlgen/plpgsql"

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

// objectFilterGuard returns the prelude statement rejecting a malformed filter.
// A filter that parsed to garbage would silently widen the result set past what
// the caller scoped, so a bad filter is an error rather than a no-op.
func objectFilterGuard() plpgsql.Stmt {
	return plpgsql.RawStmt{SQLText: `IF p_filter IS NOT NULL AND (
        position('@' in p_filter) = 0
        OR position(':' in ` + filterSubjectVar + `) = 0
        OR ` + filterRelationVar + ` = ''
        OR ` + filterSubjectTypeVar + ` = ''
        OR ` + filterSubjectIDVar + ` = ''
        OR position('#' in ` + filterSubjectIDVar + `) > 0
    ) THEN
        RAISE EXCEPTION 'melange: invalid object filter %, expected "relation@subject_type:subject_id" naming a direct relation', p_filter
            USING ERRCODE = '22023';
    END IF;`}
}

// objectFilterPrelude returns the declarations and guard every generated
// list_objects function needs before its query body.
func objectFilterPrelude() ([]plpgsql.Decl, []plpgsql.Stmt) {
	return objectFilterDecls(), []plpgsql.Stmt{
		Comment{Text: `Object filter: "relation@subject_type:subject_id" (NULL = no filter)`},
		objectFilterGuard(),
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

// partObjectIDExpr returns the expression a query projects as its object id.
// Queries assembled from the tuple builder carry it as a prefixed string column
// ("t.object_id"); queries wrapping a UNION or INTERSECT carry it as a typed
// column expression. Returns nil for anything that does not project exactly one
// column, which the caller treats as "not filterable".
func partObjectIDExpr(stmt SelectStmt) Expr {
	switch {
	case len(stmt.ColumnExprs) == 1:
		return stmt.ColumnExprs[0]
	case len(stmt.Columns) == 1:
		return Raw(stmt.Columns[0])
	default:
		return nil
	}
}

// filterPartQuery ANDs the object filter onto a query using whatever it
// projects as its object id.
//
// Used for INTERSECT parts: filter(A INTERSECT B) equals
// filter(A) INTERSECT filter(B), so constraining every part is sound and bounds
// each input before the set operation, where the cost actually is.
func filterPartQuery(plan ListPlan, stmt SelectStmt) SelectStmt {
	idExpr := partObjectIDExpr(stmt)
	if idExpr == nil {
		return stmt
	}
	pred := objectFilterPredicate(plan, idExpr)
	if stmt.Where == nil {
		stmt.Where = pred
	} else {
		stmt.Where = And(stmt.Where, pred)
	}
	return stmt
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
		pred := objectFilterPredicate(plan, blocks[i].FilterIDExpr)
		if blocks[i].Query.Where == nil {
			blocks[i].Query.Where = pred
		} else {
			blocks[i].Query.Where = And(blocks[i].Query.Where, pred)
		}
	}
	return pushedDown
}
