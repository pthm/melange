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

// InternalPermissionCheckCall creates a check_permission_internal function call comparison.
// This is the most common pattern in the codebase for recursive permission checks.
//
// Example:
//
//	InternalPermissionCheckCall("viewer", "document", Col{Table: "t", Column: "object_id"}, Visited)
//
// Renders: check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'document', t.object_id, p_visited) = 1
func InternalPermissionCheckCall(schema, relation, objectType string, objectID, visited Expr) FuncCallEq {
	return FuncCallEq{
		Schema:   schema,
		FuncName: "check_permission_internal",
		Args: []Expr{
			SubjectType,
			SubjectID,
			Lit(relation),
			Lit(objectType),
			objectID,
			visited,
		},
		Value: Int(1),
	}
}

// NoWildcardPermissionCheckCall creates a check_permission_nw_internal call
// with an empty visited array. It targets the internal dispatcher directly
// rather than the public check_permission_nw wrapper so the per-candidate-row
// validation in list_subjects skips the extra LANGUAGE sql wrapper layer (and
// its GUC save/restore). Semantics are identical: the nw dispatcher excludes
// wildcard grants, and visited starts empty at a fresh check.
func NoWildcardPermissionCheckCall(schema, relation, objectType string, subjectID, objectID Expr) FuncCallEq {
	return FuncCallEq{
		Schema:   schema,
		FuncName: "check_permission_nw_internal",
		Args: []Expr{
			SubjectType,
			subjectID,
			Lit(relation),
			Lit(objectType),
			objectID,
			EmptyArray{},
		},
		Value: Int(1),
	}
}

// WildcardPermissionCheckCall creates a full check_permission_internal call
// (WITH wildcard grants) for a specific subject id, with an empty visited array.
// Unlike NoWildcardPermissionCheckCall it does not exclude wildcard grants, so it
// is the correct verifier for the '*' subject itself in list_subjects: the
// wildcard's access legitimately flows through wildcard ([user:*]) grants, and it
// must still be subtracted when an exclusion denies it (e.g. `viewer but not
// blocked` with blocked:[user:*]).
func WildcardPermissionCheckCall(schema, relation, objectType string, subjectID, objectID Expr) FuncCallEq {
	return FuncCallEq{
		Schema:   schema,
		FuncName: "check_permission_internal",
		Args: []Expr{
			SubjectType,
			subjectID,
			Lit(relation),
			Lit(objectType),
			objectID,
			EmptyArray{},
		},
		Value: Int(1),
	}
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

// InternalCheckCall creates a check_permission_internal call with explicit subject/object types.
// Used in parent relation (TTU) checks where the linking tuple provides the parent object.
//
// Example:
//
//	InternalCheckCall(SubjectType, SubjectID, "viewer", Col{Table: "link", Column: "subject_type"}, Col{Table: "link", Column: "subject_id"}, visited)
//
// Renders: check_permission_internal(p_subject_type, p_subject_id, 'viewer', link.subject_type, link.subject_id, <visited>) = 1
func InternalCheckCall(schema string, subjectType, subjectID Expr, relation string, parentType, parentID, visited Expr) FuncCallEq {
	return FuncCallEq{
		Schema:   schema,
		FuncName: "check_permission_internal",
		Args: []Expr{
			subjectType,
			subjectID,
			Lit(relation),
			parentType,
			parentID,
			visited,
		},
		Value: Int(1),
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
type InFunctionSelect struct {
	Expr      Expr // The expression to check (left side of IN)
	Schema    string
	FuncName  string // The function to call
	Args      []Expr // Function arguments
	Alias     string // Alias for the function result
	SelectCol string // Column to select from the function result
}

// SQL renders the IN subquery expression.
func (i InFunctionSelect) SQL() string {
	funcCall := Func{Schema: i.Schema, Name: i.FuncName, Args: i.Args}.SQL()
	subquery := "SELECT " + i.Alias + "." + i.SelectCol + " FROM " + funcCall + " " + i.Alias
	return i.Expr.SQL() + " IN (" + subquery + ")"
}

// InCTESelect represents "expr IN (SELECT column FROM cte)". It is the
// hoisted-CTE counterpart to InFunctionSelect: instead of inlining a
// list_*_obj function call, it references a CTE that computed that call once.
//
// Renders: t.subject_id IN (SELECT object_id FROM folder_editor_objs)
type InCTESelect struct {
	Expr      Expr   // The expression to check (left side of IN)
	CTEName   string // The CTE to select from
	SelectCol string // Column to select from the CTE
}

// SQL renders the IN subquery expression against a CTE. The selected column is
// qualified with the CTE name so it never collides with an outer query column
// of the same name (e.g. object_id in a WITH RECURSIVE list_objects body).
func (i InCTESelect) SQL() string {
	return i.Expr.SQL() + " IN (SELECT " + i.CTEName + "." + i.SelectCol + " FROM " + i.CTEName + ")"
}

// ListObjectsFunctionName generates a list_TYPE_RELATION_obj function name.
func ListObjectsFunctionName(objectType, relation string) string {
	return SafeIdentifier("list_", objectType, relation, "_obj")
}

// ListSubjectsFunctionName generates a list_TYPE_RELATION_sub function name.
func ListSubjectsFunctionName(objectType, relation string) string {
	return SafeIdentifier("list_", objectType, relation, "_sub")
}
