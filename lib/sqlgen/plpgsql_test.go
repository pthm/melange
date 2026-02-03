package sqlgen

import (
	"strings"
	"testing"
)

func TestStmtTypes(t *testing.T) {
	tests := []struct {
		name   string
		stmt   Stmt
		expect string
	}{
		{
			name:   "return query",
			stmt:   ReturnQuery{Query: "SELECT * FROM users"},
			expect: "RETURN QUERY\n    SELECT * FROM users;",
		},
		{
			name:   "return",
			stmt:   Return{},
			expect: "RETURN;",
		},
		{
			name:   "assign",
			stmt:   Assign{Name: "v_count", Value: Int(42)},
			expect: "v_count := 42;",
		},
		{
			name:   "assign expr",
			stmt:   Assign{Name: "v_type", Value: Lit("user")},
			expect: "v_type := 'user';",
		},
		{
			name:   "raise",
			stmt:   Raise{Message: "resolution too complex", ErrCode: "M2002"},
			expect: "RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';",
		},
		{
			name:   "raw stmt",
			stmt:   RawStmt{SQLText: "PERFORM some_function();"},
			expect: "PERFORM some_function();",
		},
		{
			name:   "comment",
			stmt:   Comment{Text: "This is a comment"},
			expect: "-- This is a comment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stmt.StmtSQL()
			if got != tt.expect {
				t.Errorf("StmtSQL() =\n%q\nwant:\n%q", got, tt.expect)
			}
		})
	}
}

func TestIfStmt(t *testing.T) {
	t.Run("if then only", func(t *testing.T) {
		stmt := If{
			Cond: Raw("p_value > 0"),
			Then: []Stmt{
				Return{},
			},
		}
		got := stmt.StmtSQL()
		expect := "IF p_value > 0 THEN\n    RETURN;\nEND IF;"
		if got != expect {
			t.Errorf("StmtSQL() =\n%q\nwant:\n%q", got, expect)
		}
	})

	t.Run("if then else", func(t *testing.T) {
		stmt := If{
			Cond: Raw("p_value > 0"),
			Then: []Stmt{
				ReturnQuery{Query: "SELECT 1"},
			},
			Else: []Stmt{
				Return{},
			},
		}
		got := stmt.StmtSQL()
		expect := "IF p_value > 0 THEN\n    RETURN QUERY\n    SELECT 1;\nELSE\n    RETURN;\nEND IF;"
		if got != expect {
			t.Errorf("StmtSQL() =\n%q\nwant:\n%q", got, expect)
		}
	})
}

func TestPlpgsqlFunction(t *testing.T) {
	t.Run("simple function", func(t *testing.T) {
		fn := PlpgsqlFunction{
			Name:    "test_function",
			Args:    []FuncArg{{Name: "p_id", Type: "TEXT"}},
			Returns: "INT",
			Body: []Stmt{
				ReturnQuery{Query: "SELECT 1"},
			},
		}
		got := fn.SQL()

		// Check key parts
		if !strings.Contains(got, "CREATE OR REPLACE FUNCTION test_function(") {
			t.Error("missing function declaration")
		}
		if !strings.Contains(got, "p_id TEXT") {
			t.Error("missing argument")
		}
		if !strings.Contains(got, "RETURNS INT AS $$") {
			t.Error("missing returns clause")
		}
		if !strings.Contains(got, "BEGIN") {
			t.Error("missing BEGIN")
		}
		if !strings.Contains(got, "RETURN QUERY") {
			t.Error("missing RETURN QUERY")
		}
		if !strings.Contains(got, "END;") {
			t.Error("missing END")
		}
		if !strings.Contains(got, "$$ LANGUAGE plpgsql STABLE;") {
			t.Error("missing language spec")
		}
	})

	t.Run("function with defaults", func(t *testing.T) {
		fn := PlpgsqlFunction{
			Name: "test_function",
			Args: []FuncArg{
				{Name: "p_limit", Type: "INT", Default: Null{}},
				{Name: "p_name", Type: "TEXT", Default: Lit("default")},
			},
			Returns: "INT",
			Body:    []Stmt{Return{}},
		}
		got := fn.SQL()

		if !strings.Contains(got, "p_limit INT DEFAULT NULL") {
			t.Errorf("missing NULL default, got:\n%s", got)
		}
		if !strings.Contains(got, "p_name TEXT DEFAULT 'default'") {
			t.Errorf("missing string default, got:\n%s", got)
		}
	})

	t.Run("function with declarations", func(t *testing.T) {
		fn := PlpgsqlFunction{
			Name:    "test_function",
			Args:    []FuncArg{{Name: "p_id", Type: "TEXT"}},
			Returns: "INT",
			Decls: []Decl{
				{Name: "v_count", Type: "INT"},
				{Name: "v_name", Type: "TEXT"},
			},
			Body: []Stmt{Return{}},
		}
		got := fn.SQL()

		if !strings.Contains(got, "DECLARE") {
			t.Error("missing DECLARE")
		}
		if !strings.Contains(got, "v_count INT;") {
			t.Error("missing v_count declaration")
		}
		if !strings.Contains(got, "v_name TEXT;") {
			t.Error("missing v_name declaration")
		}
	})

	t.Run("function with header", func(t *testing.T) {
		fn := PlpgsqlFunction{
			Name:    "test_function",
			Args:    []FuncArg{{Name: "p_id", Type: "TEXT"}},
			Returns: "INT",
			Header: []string{
				"Generated function for testing",
				"Features: basic",
			},
			Body: []Stmt{Return{}},
		}
		got := fn.SQL()

		if !strings.Contains(got, "-- Generated function for testing") {
			t.Error("missing first header comment")
		}
		if !strings.Contains(got, "-- Features: basic") {
			t.Error("missing second header comment")
		}
	})

	t.Run("list objects function pattern", func(t *testing.T) {
		fn := PlpgsqlFunction{
			Name:    "list_document_viewer_objects",
			Args:    ListObjectsArgs(),
			Returns: ListObjectsReturns(),
			Header:  ListObjectsFunctionHeader("document", "viewer", "Direct"),
			Body: []Stmt{
				ReturnQuery{Query: "SELECT object_id FROM results"},
			},
		}
		got := fn.SQL()

		// Verify the structure matches expected pattern
		if !strings.Contains(got, "p_subject_type TEXT") {
			t.Error("missing p_subject_type")
		}
		if !strings.Contains(got, "p_subject_id TEXT") {
			t.Error("missing p_subject_id")
		}
		if !strings.Contains(got, "p_limit INT DEFAULT NULL") {
			t.Error("missing p_limit with default")
		}
		if !strings.Contains(got, "p_after TEXT DEFAULT NULL") {
			t.Error("missing p_after with default")
		}
		if !strings.Contains(got, "TABLE(object_id TEXT, next_cursor TEXT)") {
			t.Error("missing returns table")
		}
	})
}

func TestListObjectsHelpers(t *testing.T) {
	args := ListObjectsArgs()
	if len(args) != 4 {
		t.Errorf("ListObjectsArgs() = %d args, want 4", len(args))
	}

	returns := ListObjectsReturns()
	if returns != "TABLE(object_id TEXT, next_cursor TEXT) ROWS 100" {
		t.Errorf("ListObjectsReturns() = %q", returns)
	}
}

func TestListSubjectsHelpers(t *testing.T) {
	args := ListSubjectsArgs()
	if len(args) != 4 {
		t.Errorf("ListSubjectsArgs() = %d args, want 4", len(args))
	}

	returns := ListSubjectsReturns()
	if returns != "TABLE(subject_id TEXT, next_cursor TEXT) ROWS 100" {
		t.Errorf("ListSubjectsReturns() = %q", returns)
	}
}
