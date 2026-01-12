package sqldsl

import "strings"

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
	FuncName string
	Args     []Expr
	Value    Expr // The value to compare against (typically Int(1) or Int(0))
}

// SQL renders the function call comparison.
func (f FuncCallEq) SQL() string {
	args := make([]string, len(f.Args))
	for i, arg := range f.Args {
		args[i] = arg.SQL()
	}
	return f.FuncName + "(" + strings.Join(args, ", ") + ") = " + f.Value.SQL()
}

// FuncCallNe compares a function call result for inequality.
// Used for negative authorization checks like "check_permission(...) <> 1".
type FuncCallNe struct {
	FuncName string
	Args     []Expr
	Value    Expr
}

// SQL renders the function call not-equal comparison.
func (f FuncCallNe) SQL() string {
	args := make([]string, len(f.Args))
	for i, arg := range f.Args {
		args[i] = arg.SQL()
	}
	return f.FuncName + "(" + strings.Join(args, ", ") + ") <> " + f.Value.SQL()
}

// InternalPermissionCheckCall creates a check_permission_internal function call comparison.
// This is the most common pattern in the codebase for recursive permission checks.
//
// Example:
//
//	InternalPermissionCheckCall("viewer", "document", Col{Table: "t", Column: "object_id"}, Visited)
//
// Renders: check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'document', t.object_id, p_visited) = 1
func InternalPermissionCheckCall(relation, objectType string, objectID, visited Expr) FuncCallEq {
	return FuncCallEq{
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

// NoWildcardPermissionCheckCall creates a check_permission_no_wildcard function call.
func NoWildcardPermissionCheckCall(relation, objectType string, subjectID, objectID Expr) FuncCallEq {
	return FuncCallEq{
		FuncName: "check_permission_no_wildcard",
		Args: []Expr{
			SubjectType,
			subjectID,
			Lit(relation),
			Lit(objectType),
			objectID,
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
func SpecializedCheckCall(funcName string, subjectType, subjectID, objectID, visited Expr) FuncCallEq {
	return FuncCallEq{
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
func InternalCheckCall(subjectType, subjectID Expr, relation string, parentType, parentID, visited Expr) FuncCallEq {
	return FuncCallEq{
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
//	    FuncName:   "list_doc_viewer_objects",
//	    Args:       []Expr{SubjectType, SubjectID, Null{}, Null{}},
//	    Alias:      "obj",
//	    SelectCol:  "object_id",
//	}
//
// Renders: t.subject_id IN (SELECT obj.object_id FROM list_doc_viewer_objects(p_subject_type, p_subject_id, NULL, NULL) obj)
type InFunctionSelect struct {
	Expr      Expr   // The expression to check (left side of IN)
	FuncName  string // The function to call
	Args      []Expr // Function arguments
	Alias     string // Alias for the function result
	SelectCol string // Column to select from the function result
}

// SQL renders the IN subquery expression.
func (i InFunctionSelect) SQL() string {
	args := make([]string, len(i.Args))
	for j, arg := range i.Args {
		args[j] = arg.SQL()
	}
	subquery := "SELECT " + i.Alias + "." + i.SelectCol + " FROM " + i.FuncName + "(" + strings.Join(args, ", ") + ") " + i.Alias
	return i.Expr.SQL() + " IN (" + subquery + ")"
}

// ListObjectsFunctionName generates a list_TYPE_RELATION_objects function name.
func ListObjectsFunctionName(objectType, relation string) string {
	return "list_" + objectType + "_" + relation + "_objects"
}

// ListSubjectsFunctionName generates a list_TYPE_RELATION_subjects function name.
func ListSubjectsFunctionName(objectType, relation string) string {
	return "list_" + objectType + "_" + relation + "_subjects"
}
