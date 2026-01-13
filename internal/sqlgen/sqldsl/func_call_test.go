package sqldsl

import (
	"strings"
	"testing"
)

func TestFuncCallEq_SQL(t *testing.T) {
	tests := []struct {
		name string
		fc   FuncCallEq
		want string
	}{
		{
			name: "simple check function",
			fc: FuncCallEq{
				FuncName: "check_doc_viewer",
				Args:     []Expr{SubjectType, SubjectID, ObjectID, Visited},
				Value:    Int(1),
			},
			want: "check_doc_viewer(p_subject_type, p_subject_id, p_object_id, p_visited) = 1",
		},
		{
			name: "with literal args",
			fc: FuncCallEq{
				FuncName: "check_permission_internal",
				Args:     []Expr{SubjectType, SubjectID, Lit("viewer"), Lit("document"), ObjectID, Visited},
				Value:    Int(1),
			},
			want: "check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'document', p_object_id, p_visited) = 1",
		},
		{
			name: "compare to zero",
			fc: FuncCallEq{
				FuncName: "my_func",
				Args:     []Expr{Lit("test")},
				Value:    Int(0),
			},
			want: "my_func('test') = 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fc.SQL(); got != tt.want {
				t.Errorf("FuncCallEq.SQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFuncCallNe_SQL(t *testing.T) {
	fc := FuncCallNe{
		FuncName: "check_permission",
		Args:     []Expr{SubjectType, SubjectID, Lit("admin"), Lit("system"), ObjectID},
		Value:    Int(1),
	}

	got := fc.SQL()
	if !strings.Contains(got, "<> 1") {
		t.Errorf("FuncCallNe.SQL() = %q, want to contain '<> 1'", got)
	}
}

func TestInternalPermissionCheckCall(t *testing.T) {
	fc := InternalPermissionCheckCall("viewer", "document", Col{Table: "t", Column: "object_id"}, Visited)
	got := fc.SQL()

	expected := []string{
		"check_permission_internal(",
		"p_subject_type",
		"p_subject_id",
		"'viewer'",
		"'document'",
		"t.object_id",
		"p_visited",
		") = 1",
	}

	for _, want := range expected {
		if !strings.Contains(got, want) {
			t.Errorf("CheckPermissionCall().SQL() = %q, want to contain %q", got, want)
		}
	}
}

func TestSpecializedCheckCall(t *testing.T) {
	fc := SpecializedCheckCall("check_doc_owner", SubjectType, SubjectID, ObjectID, Visited)
	got := fc.SQL()

	if got != "check_doc_owner(p_subject_type, p_subject_id, p_object_id, p_visited) = 1" {
		t.Errorf("SpecializedCheckCall().SQL() = %q", got)
	}
}

func TestInternalCheckCall(t *testing.T) {
	fc := InternalCheckCall(
		SubjectType,
		SubjectID,
		"viewer",
		Col{Table: "link", Column: "subject_type"},
		Col{Table: "link", Column: "subject_id"},
		Visited,
	)
	got := fc.SQL()

	expected := []string{
		"check_permission_internal(",
		"p_subject_type",
		"p_subject_id",
		"'viewer'",
		"link.subject_type",
		"link.subject_id",
		"p_visited",
		") = 1",
	}

	for _, want := range expected {
		if !strings.Contains(got, want) {
			t.Errorf("InternalCheckCall().SQL() = %q, want to contain %q", got, want)
		}
	}
}

func TestInFunctionSelect_SQL(t *testing.T) {
	tests := []struct {
		name string
		in   InFunctionSelect
		want string
	}{
		{
			name: "list objects function",
			in: InFunctionSelect{
				Expr:      Col{Table: "t", Column: "subject_id"},
				FuncName:  "list_doc_viewer_objects",
				Args:      []Expr{SubjectType, SubjectID, Null{}, Null{}},
				Alias:     "obj",
				SelectCol: "object_id",
			},
			want: "t.subject_id IN (SELECT obj.object_id FROM list_doc_viewer_objects(p_subject_type, p_subject_id, NULL, NULL) obj)",
		},
		{
			name: "split_part expression",
			in: InFunctionSelect{
				Expr:      Raw("split_part(t.subject_id, '#', 1)"),
				FuncName:  "list_group_member_objects",
				Args:      []Expr{SubjectType, SubjectID, Null{}, Null{}},
				Alias:     "obj",
				SelectCol: "object_id",
			},
			want: "split_part(t.subject_id, '#', 1) IN (SELECT obj.object_id FROM list_group_member_objects(p_subject_type, p_subject_id, NULL, NULL) obj)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.SQL(); got != tt.want {
				t.Errorf("InFunctionSelect.SQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListObjectsFunctionName(t *testing.T) {
	got := ListObjectsFunctionName("document", "viewer")
	want := "list_document_viewer_objects"
	if got != want {
		t.Errorf("ListObjectsFunctionName() = %q, want %q", got, want)
	}
}

func TestListSubjectsFunctionName(t *testing.T) {
	got := ListSubjectsFunctionName("document", "viewer")
	want := "list_document_viewer_subjects"
	if got != want {
		t.Errorf("ListSubjectsFunctionName() = %q, want %q", got, want)
	}
}
