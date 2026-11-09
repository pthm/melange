package sqldsl

// FuncCallEq compares a function call result to a value.
// Used for authorization check expressions like "check_permission(...) = 1".
//
// Example:
//
//	FuncCallEq{
//	    FuncName: "check_doc_viewer",
//	    Args:     []Expr{SubjectType, SubjectID, ObjectID, Visited},
//	    Value:    Int(1),
//	}
//
// Renders: check_doc_viewer(p_subject_type, p_subject_id, p_object_id, p_visited) = 1
type FuncCallEq struct {
	Schema   string
	FuncName string
	Args     []Expr
	Value    Expr // The value to compare against (typically Int(1) or Int(0))
}

// SQL renders the function call comparison.
func (f FuncCallEq) SQL() string {
	return Eq{Left: Func{Schema: f.Schema, Name: f.FuncName, Args: f.Args}, Right: f.Value}.SQL()
}

// FuncCallNe compares a function call result for inequality.
// Used for negative authorization checks like "check_permission(...) <> 1".
type FuncCallNe struct {
	Schema   string
	FuncName string
	Args     []Expr
	Value    Expr
}

// SQL renders the function call not-equal comparison.
func (f FuncCallNe) SQL() string {
	return Ne{Left: Func{Schema: f.Schema, Name: f.FuncName, Args: f.Args}, Right: f.Value}.SQL()
}

// SpecializedCheckCall creates a call to a specialized check function.
// Used for implied relations and parent relation checks.
//
// Example:
//
//	SpecializedCheckCall("check_doc_owner", SubjectType, SubjectID, ObjectID, Visited)
//
// Renders: check_doc_owner(p_subject_type, p_subject_id, p_object_id, p_visited) = 1
func SpecializedCheckCall(schema, funcName string, subjectType, subjectID, objectID, visited Expr) FuncCallEq {
	return FuncCallEq{
		Schema:   schema,
		FuncName: funcName,
		Args:     []Expr{subjectType, subjectID, objectID, visited},
		Value:    Int(1),
	}
}

// InFunctionSelect represents "expr IN (SELECT column FROM func(args...) alias)".
// Used for checking membership against results of a list function.
//
// Example:
//
//	InFunctionSelect{
//	    Expr:       Col{Table: "t", Column: "subject_id"},
//	    Schema:     "public",
//	    FuncName:   "list_doc_viewer_obj",
//	    Args:       []Expr{SubjectType, SubjectID, Null{}, Null{}},
//	    Alias:      "obj",
//	    SelectCol:  "object_id",
//	}
//
// Renders: t.subject_id IN (SELECT obj.object_id FROM list_doc_viewer_obj(p_subject_type, p_subject_id, NULL, NULL) obj)
//
// With Array set it renders "expr = ANY(ARRAY(subquery))" instead, for a
// lookup keyed by expr. PostgreSQL evaluates the uncorrelated ARRAY subquery
// once and compares expr with it as a plain filter, which it pushes into every
// branch of a UNION ALL tuples view; a branch with a WHERE clause or a join is
// never pulled up into the query, so an IN semi-join can only scan it in full,
// while the filter reaches the branch's index on expr. The array is searched
// linearly, so it suits only a filter the index answers, not one applied to
// each row of a scan, where IN hashes the set.
type InFunctionSelect struct {
	Expr      Expr // The expression to check (left side of IN)
	Schema    string
	FuncName  string // The function to call
	Args      []Expr // Function arguments
	Alias     string // Alias for the function result
	SelectCol string // Column to select from the function result
	Array     bool   // Render = ANY(ARRAY(...)) for an index-keyed lookup
}

// SQL renders the membership expression.
func (i InFunctionSelect) SQL() string {
	funcCall := Func{Schema: i.Schema, Name: i.FuncName, Args: i.Args}.SQL()
	return memberOf(i.Expr, "SELECT "+i.Alias+"."+i.SelectCol+" FROM "+funcCall+" "+i.Alias, i.Array)
}

// InCTESelect represents "expr IN (SELECT column FROM cte)". It is the
// hoisted-CTE counterpart to InFunctionSelect: instead of inlining a
// list_*_obj function call, it references a CTE that computed that call once.
//
// Renders: t.subject_id IN (SELECT folder_editor_objs.object_id FROM folder_editor_objs)
type InCTESelect struct {
	Expr      Expr   // The expression to check (left side of IN)
	CTEName   string // The CTE to select from
	SelectCol string // Column to select from the CTE
	Array     bool   // Render = ANY(ARRAY(...)), as InFunctionSelect.Array
}

// SQL renders the membership expression against a CTE. The selected column is
// qualified with the CTE name so it never collides with an outer query column
// of the same name (e.g. object_id in a WITH RECURSIVE list_objects body).
func (i InCTESelect) SQL() string {
	return memberOf(i.Expr, "SELECT "+i.CTEName+"."+i.SelectCol+" FROM "+i.CTEName, i.Array)
}

// memberOf renders "expr IN (subquery)", or with array the equivalent
// "expr = ANY(ARRAY(subquery))" (the same result, NULLs included).
func memberOf(expr Expr, subquery string, array bool) string {
	if array {
		return expr.SQL() + " = ANY(ARRAY(" + subquery + "))"
	}
	return expr.SQL() + " IN (" + subquery + ")"
}

// ListObjectsFunctionName generates a list_TYPE_RELATION_obj function name.
func ListObjectsFunctionName(objectType, relation string) string {
	return SafeIdentifier("list_", objectType, relation, "_obj")
}

// ListSubjectsFunctionName generates a list_TYPE_RELATION_sub function name.
func ListSubjectsFunctionName(objectType, relation string) string {
	return SafeIdentifier("list_", objectType, relation, "_sub")
}
