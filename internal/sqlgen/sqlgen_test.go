package sqlgen

import (
	"strings"
	"testing"
)

func TestSqlf(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		args   []any
		expect string
	}{
		{
			name: "dedent simple",
			input: `
				SELECT *
				FROM users
			`,
			expect: "SELECT *\nFROM users",
		},
		{
			name: "with format args",
			input: `
				SELECT %s
				FROM %s
			`,
			args:   []any{"name", "users"},
			expect: "SELECT name\nFROM users",
		},
		{
			name:   "single line",
			input:  "SELECT 1",
			expect: "SELECT 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqlf(tt.input, tt.args...)
			if got != tt.expect {
				t.Errorf("sqlf() =\n%q\nwant:\n%q", got, tt.expect)
			}
		})
	}
}

func TestOptf(t *testing.T) {
	if got := optf(true, "DISTINCT "); got != "DISTINCT " {
		t.Errorf("optf(true) = %q, want %q", got, "DISTINCT ")
	}
	if got := optf(false, "DISTINCT "); got != "" {
		t.Errorf("optf(false) = %q, want %q", got, "")
	}
	if got := optf(true, "LIMIT %d", 10); got != "LIMIT 10" {
		t.Errorf("optf with args = %q, want %q", got, "LIMIT 10")
	}
}

func TestExprTypes(t *testing.T) {
	tests := []struct {
		name   string
		expr   Expr
		expect string
	}{
		// Param
		{"param", SubjectType, "p_subject_type"},
		{"param custom", Param("p_custom"), "p_custom"},

		// Col
		{"col with table", Col{Table: "t", Column: "id"}, "t.id"},
		{"col without table", Col{Column: "id"}, "id"},

		// Lit
		{"lit simple", Lit("hello"), "'hello'"},
		{"lit with quote", Lit("it's"), "'it''s'"},

		// Raw
		{"raw", Raw("NOW()"), "NOW()"},

		// Int
		{"int", Int(42), "42"},

		// Bool
		{"bool true", Bool(true), "TRUE"},
		{"bool false", Bool(false), "FALSE"},

		// Null
		{"null", Null{}, "NULL"},

		// EmptyArray
		{"empty array", EmptyArray{}, "ARRAY[]::TEXT[]"},

		// Func
		{"func", Func{Name: "count", Args: []Expr{Col{Column: "*"}}}, "count(*)"},
		{"func multi args", Func{Name: "coalesce", Args: []Expr{Col{Column: "a"}, Lit("default")}}, "coalesce(a, 'default')"},

		// Alias
		{"alias", Alias{Expr: Col{Column: "name"}, Name: "n"}, "name AS n"},

		// Paren
		{"paren", Paren{Expr: Raw("a + b")}, "(a + b)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.expr.SQL()
			if got != tt.expect {
				t.Errorf("SQL() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestOperators(t *testing.T) {
	tests := []struct {
		name   string
		expr   Expr
		expect string
	}{
		// Eq
		{"eq", Eq{Col{Table: "t", Column: "id"}, Lit("1")}, "t.id = '1'"},

		// Ne
		{"ne", Ne{Col{Column: "status"}, Lit("deleted")}, "status <> 'deleted'"},

		// Lt, Gt, Lte, Gte
		{"lt", Lt{Col{Column: "age"}, Int(18)}, "age < 18"},
		{"gt", Gt{Col{Column: "score"}, Int(100)}, "score > 100"},
		{"lte", Lte{Col{Column: "count"}, Int(10)}, "count <= 10"},
		{"gte", Gte{Col{Column: "amount"}, Int(0)}, "amount >= 0"},

		// In
		{"in", In{Expr: Col{Column: "status"}, Values: []string{"a", "b"}}, "status IN ('a', 'b')"},
		{"in empty", In{Expr: Col{Column: "x"}, Values: []string{}}, "FALSE"},

		// And
		{"and single", And(Eq{Col{Column: "a"}, Int(1)}), "a = 1"},
		{"and multiple", And(Eq{Col{Column: "a"}, Int(1)}, Eq{Col{Column: "b"}, Int(2)}), "(a = 1 AND b = 2)"},
		{"and empty", And(), "TRUE"},

		// Or
		{"or single", Or(Eq{Col{Column: "a"}, Int(1)}), "a = 1"},
		{"or multiple", Or(Eq{Col{Column: "a"}, Int(1)}, Eq{Col{Column: "b"}, Int(2)}), "(a = 1 OR b = 2)"},
		{"or empty", Or(), "FALSE"},

		// Not
		{"not", Not(Eq{Col{Column: "active"}, Bool(true)}), "NOT (active = TRUE)"},

		// IsNull, IsNotNull
		{"is null", IsNull{Col{Column: "deleted_at"}}, "deleted_at IS NULL"},
		{"is not null", IsNotNull{Col{Column: "name"}}, "name IS NOT NULL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.expr.SQL()
			if got != tt.expect {
				t.Errorf("SQL() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestUsersetOperations(t *testing.T) {
	tests := []struct {
		name   string
		expr   Expr
		expect string
	}{
		{
			name:   "userset object id",
			expr:   UsersetObjectID{Col{Table: "t", Column: "subject_id"}},
			expect: "split_part(t.subject_id, '#', 1)",
		},
		{
			name:   "userset relation",
			expr:   UsersetRelation{Col{Table: "t", Column: "subject_id"}},
			expect: "split_part(t.subject_id, '#', 2)",
		},
		{
			name:   "has userset",
			expr:   HasUserset{Col{Table: "t", Column: "subject_id"}},
			expect: "position('#' in t.subject_id) > 0",
		},
		{
			name:   "no userset",
			expr:   NoUserset{SubjectID},
			expect: "position('#' in p_subject_id) = 0",
		},
		{
			name:   "substring userset relation",
			expr:   SubstringUsersetRelation{SubjectID},
			expect: "substring(p_subject_id from position('#' in p_subject_id) + 1)",
		},
		{
			name:   "is wildcard",
			expr:   IsWildcard{Col{Column: "subject_id"}},
			expect: "subject_id = '*'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.expr.SQL()
			if got != tt.expect {
				t.Errorf("SQL() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestSubjectIDMatch(t *testing.T) {
	col := Col{Table: "t", Column: "subject_id"}

	// With wildcard
	got := SubjectIDMatch(col, SubjectID, true).SQL()
	if !strings.Contains(got, "t.subject_id = p_subject_id") || !strings.Contains(got, "t.subject_id = '*'") {
		t.Errorf("SubjectIDMatch(allowWildcard=true) = %q, missing expected parts", got)
	}

	// Without wildcard
	got = SubjectIDMatch(col, SubjectID, false).SQL()
	if !strings.Contains(got, "t.subject_id = p_subject_id") || !strings.Contains(got, "NOT") {
		t.Errorf("SubjectIDMatch(allowWildcard=false) = %q, missing expected parts", got)
	}
}

func TestStringFunctions(t *testing.T) {
	tests := []struct {
		name   string
		expr   Expr
		expect string
	}{
		// Concat
		{
			name:   "concat two parts",
			expr:   Concat{Parts: []Expr{Col{Column: "a"}, Lit("b")}},
			expect: "a || 'b'",
		},
		{
			name:   "concat multiple",
			expr:   Concat{Parts: []Expr{Col{Column: "a"}, Lit("#"), Col{Column: "b"}}},
			expect: "a || '#' || b",
		},
		{
			name:   "concat empty",
			expr:   Concat{Parts: []Expr{}},
			expect: "''",
		},

		// Position
		{
			name:   "position",
			expr:   Position{Needle: Lit("#"), Haystack: Col{Table: "t", Column: "subject_id"}},
			expect: "position('#' in t.subject_id)",
		},
		{
			name:   "position with param",
			expr:   Position{Needle: Lit("#"), Haystack: SubjectID},
			expect: "position('#' in p_subject_id)",
		},

		// Substring without For
		{
			name:   "substring from",
			expr:   Substring{Source: Col{Column: "text"}, From: Int(5)},
			expect: "substring(text from 5)",
		},
		{
			name:   "substring with position",
			expr:   Substring{Source: SubjectID, From: Position{Needle: Lit("#"), Haystack: SubjectID}},
			expect: "substring(p_subject_id from position('#' in p_subject_id))",
		},

		// Substring with For
		{
			name:   "substring from for",
			expr:   Substring{Source: Col{Column: "text"}, From: Int(1), For: Int(10)},
			expect: "substring(text from 1 for 10)",
		},

		// UsersetNormalized
		{
			name:   "userset normalized",
			expr:   UsersetNormalized{Source: Col{Table: "t", Column: "subject_id"}, Relation: Raw("v_filter_relation")},
			expect: "substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation",
		},
		{
			name:   "userset normalized with literal",
			expr:   UsersetNormalized{Source: SubjectID, Relation: Lit("member")},
			expect: "substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) || '#' || 'member'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.expr.SQL()
			if got != tt.expect {
				t.Errorf("SQL() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	// ParamRef
	p := ParamRef("v_filter_type")
	if got := p.SQL(); got != "v_filter_type" {
		t.Errorf("ParamRef.SQL() = %q, want %q", got, "v_filter_type")
	}

	// LitText
	l := LitText("document")
	if got := l.SQL(); got != "'document'" {
		t.Errorf("LitText.SQL() = %q, want %q", got, "'document'")
	}

	// LitText with quote
	l2 := LitText("it's a test")
	if got := l2.SQL(); got != "'it''s a test'" {
		t.Errorf("LitText.SQL() = %q, want %q", got, "'it''s a test'")
	}
}

func TestCheckPermission(t *testing.T) {
	check := CheckPermission{
		Subject:     SubjectParams(),
		Relation:    "viewer",
		Object:      LiteralObject("document", Col{Table: "t", Column: "object_id"}),
		ExpectAllow: true,
	}
	got := check.SQL()
	expect := "check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'document', t.object_id, ARRAY[]::TEXT[]) = 1"
	if got != expect {
		t.Errorf("CheckPermission.SQL() =\n%q\nwant:\n%q", got, expect)
	}

	// Test with ExpectAllow = false
	check.ExpectAllow = false
	got = check.SQL()
	if !strings.HasSuffix(got, "= 0") {
		t.Errorf("CheckPermission with ExpectAllow=false should end with '= 0', got %q", got)
	}
}

func TestCheckAccessHelpers(t *testing.T) {
	access := CheckAccess("viewer", "document", ObjectID)
	got := access.SQL()
	if !strings.Contains(got, "'viewer'") || !strings.Contains(got, "'document'") || !strings.Contains(got, "= 1") {
		t.Errorf("CheckAccess() = %q, missing expected parts", got)
	}

	noAccess := CheckNoAccess("blocked", "document", ObjectID)
	got = noAccess.SQL()
	if !strings.Contains(got, "'blocked'") || !strings.Contains(got, "= 0") {
		t.Errorf("CheckNoAccess() = %q, missing expected parts", got)
	}
}

func TestTupleQueryBasic(t *testing.T) {
	q := Tuples("t").
		ObjectType("document").
		Relations("viewer", "editor").
		Select("t.object_id").
		Distinct()

	sql := q.SQL()

	// Check key parts
	checks := []string{
		"SELECT DISTINCT",
		"t.object_id",
		"FROM melange_tuples AS t",
		"t.object_type = 'document'",
		"t.relation IN ('viewer', 'editor')",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

func TestTupleQueryWithSubjectFilters(t *testing.T) {
	q := Tuples("t").
		ObjectType("document").
		Relations("viewer").
		WhereSubjectType(SubjectType).
		WhereSubjectID(SubjectID, true).
		Select("1").
		Limit(1)

	sql := q.SQL()

	checks := []string{
		"t.subject_type = p_subject_type",
		"t.subject_id = p_subject_id", // Or wildcard match
		"t.subject_id = '*'",          // Wildcard part
		"LIMIT 1",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

func TestTupleQueryWithJoin(t *testing.T) {
	q := Tuples("t").
		ObjectType("document").
		Select("t.object_id").
		JoinTuples("m",
			Eq{Col{Table: "m", Column: "object_type"}, Lit("group")},
			Eq{Col{Table: "m", Column: "object_id"}, UsersetObjectID{Col{Table: "t", Column: "subject_id"}}},
		)

	sql := q.SQL()

	checks := []string{
		"INNER JOIN melange_tuples AS m ON",
		"m.object_type = 'group'",
		"split_part(t.subject_id, '#', 1)",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

func TestTupleQueryWithUserset(t *testing.T) {
	q := Tuples("t").
		ObjectType("document").
		Relations("viewer").
		WhereHasUserset().
		WhereUsersetRelation("member").
		Select("1")

	sql := q.SQL()

	checks := []string{
		"position('#' in t.subject_id) > 0",
		"split_part(t.subject_id, '#', 2) = 'member'",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

func TestExistsNotExists(t *testing.T) {
	q := Tuples("excl").
		ObjectType("document").
		Relations("blocked").
		Select("1")

	existsSQL := q.ExistsSQL()
	if !strings.HasPrefix(existsSQL, "EXISTS (") {
		t.Errorf("ExistsSQL should start with 'EXISTS (', got: %s", existsSQL)
	}

	notExistsSQL := q.NotExistsSQL()
	if !strings.HasPrefix(notExistsSQL, "NOT EXISTS (") {
		t.Errorf("NotExistsSQL should start with 'NOT EXISTS (', got: %s", notExistsSQL)
	}
}

func TestSelectStmt(t *testing.T) {
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"id", "name"},
		From:     "users",
		Alias:    "u",
		Where:    Eq{Col{Table: "u", Column: "active"}, Bool(true)},
		Limit:    10,
	}

	sql := stmt.SQL()

	checks := []string{
		"SELECT DISTINCT id, name",
		"FROM users AS u",
		"WHERE u.active = TRUE",
		"LIMIT 10",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

func TestJoinClause(t *testing.T) {
	join := JoinClause{
		Type:  "LEFT",
		Table: "orders",
		Alias: "o",
		On:    Eq{Col{Table: "o", Column: "user_id"}, Col{Table: "u", Column: "id"}},
	}

	sql := join.SQL()
	expect := "LEFT JOIN orders AS o ON o.user_id = u.id"
	if sql != expect {
		t.Errorf("JoinClause.SQL() = %q, want %q", sql, expect)
	}
}

func TestQueryBlock(t *testing.T) {
	t.Run("single block with comments", func(t *testing.T) {
		block := QueryBlock{
			Comments: []string{"-- First comment", "-- Second comment"},
			SQL:      "SELECT 1 FROM users",
		}
		blocks := []QueryBlock{block}
		got := RenderBlocks(blocks)

		checks := []string{
			"    -- First comment",
			"    -- Second comment",
			"    SELECT 1 FROM users",
		}
		for _, check := range checks {
			if !strings.Contains(got, check) {
				t.Errorf("RenderBlocks missing %q:\n%s", check, got)
			}
		}
	})

	t.Run("single block without comments", func(t *testing.T) {
		block := QueryBlock{
			SQL: "SELECT id FROM documents",
		}
		got := RenderBlocks([]QueryBlock{block})
		expect := "    SELECT id FROM documents"
		if got != expect {
			t.Errorf("RenderBlocks() =\n%q\nwant:\n%q", got, expect)
		}
	})

	t.Run("empty blocks", func(t *testing.T) {
		got := RenderBlocks(nil)
		if got != "" {
			t.Errorf("RenderBlocks(nil) = %q, want empty", got)
		}
	})
}

func TestRenderUnionBlocks(t *testing.T) {
	t.Run("multiple blocks", func(t *testing.T) {
		blocks := []QueryBlock{
			{Comments: []string{"-- Block 1"}, SQL: "SELECT 1"},
			{Comments: []string{"-- Block 2"}, SQL: "SELECT 2"},
		}
		got := RenderUnionBlocks(blocks)

		// Check structure
		if !strings.Contains(got, "    UNION\n") {
			t.Errorf("RenderUnionBlocks missing UNION:\n%s", got)
		}
		if !strings.Contains(got, "-- Block 1") {
			t.Errorf("RenderUnionBlocks missing Block 1 comment:\n%s", got)
		}
		if !strings.Contains(got, "-- Block 2") {
			t.Errorf("RenderUnionBlocks missing Block 2 comment:\n%s", got)
		}
		if !strings.Contains(got, "SELECT 1") {
			t.Errorf("RenderUnionBlocks missing SELECT 1:\n%s", got)
		}
		if !strings.Contains(got, "SELECT 2") {
			t.Errorf("RenderUnionBlocks missing SELECT 2:\n%s", got)
		}
	})

	t.Run("single block no union", func(t *testing.T) {
		blocks := []QueryBlock{
			{SQL: "SELECT 1"},
		}
		got := RenderUnionBlocks(blocks)
		if strings.Contains(got, "UNION") {
			t.Errorf("RenderUnionBlocks with single block should not have UNION:\n%s", got)
		}
	})

	t.Run("empty blocks", func(t *testing.T) {
		got := RenderUnionBlocks(nil)
		if got != "" {
			t.Errorf("RenderUnionBlocks(nil) = %q, want empty", got)
		}
	})
}

func TestIndentLines(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		indent string
		expect string
	}{
		{
			name:   "single line",
			input:  "SELECT 1",
			indent: "  ",
			expect: "  SELECT 1",
		},
		{
			name:   "multiline",
			input:  "SELECT 1\nFROM users",
			indent: "    ",
			expect: "    SELECT 1\n    FROM users",
		},
		{
			name:   "empty input",
			input:  "",
			indent: "  ",
			expect: "",
		},
		{
			name:   "with leading/trailing whitespace",
			input:  "  \n  SELECT 1\n  ",
			indent: "--",
			expect: "--SELECT 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indentLines(tt.input, tt.indent)
			if got != tt.expect {
				t.Errorf("indentLines() =\n%q\nwant:\n%q", got, tt.expect)
			}
		})
	}
}

// =============================================================================
// Phase 5: Typed Values Tables Tests
// =============================================================================

func TestValuesRow(t *testing.T) {
	tests := []struct {
		name   string
		row    ValuesRow
		expect string
	}{
		{
			name:   "single value",
			row:    ValuesRow{Lit("hello")},
			expect: "('hello')",
		},
		{
			name:   "multiple values",
			row:    ValuesRow{Lit("doc"), Lit("viewer"), Lit("editor")},
			expect: "('doc', 'viewer', 'editor')",
		},
		{
			name:   "empty row",
			row:    ValuesRow{},
			expect: "()",
		},
		{
			name:   "with quote escaping",
			row:    ValuesRow{Lit("it's"), Lit("fine")},
			expect: "('it''s', 'fine')",
		},
		{
			name:   "mixed expressions",
			row:    ValuesRow{Lit("doc"), Col{Column: "id"}, Int(42)},
			expect: "('doc', id, 42)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.row.SQL()
			if got != tt.expect {
				t.Errorf("ValuesRow.SQL() = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestTypedValuesTable(t *testing.T) {
	t.Run("with rows", func(t *testing.T) {
		table := TypedValuesTable{
			Rows: []ValuesRow{
				{Lit("doc"), Lit("viewer"), Lit("editor")},
				{Lit("doc"), Lit("owner"), Lit("owner")},
			},
			Alias:   "c",
			Columns: []string{"object_type", "relation", "satisfying_relation"},
		}
		got := table.SQL()
		expect := "(VALUES ('doc', 'viewer', 'editor'), ('doc', 'owner', 'owner')) AS c(object_type, relation, satisfying_relation)"
		if got != expect {
			t.Errorf("TypedValuesTable.SQL() =\n%q\nwant:\n%q", got, expect)
		}
	})

	t.Run("empty rows with columns", func(t *testing.T) {
		table := TypedValuesTable{
			Rows:    nil,
			Alias:   "c",
			Columns: []string{"object_type", "relation", "satisfying_relation"},
		}
		got := table.SQL()
		expect := "(VALUES (NULL::TEXT, NULL::TEXT, NULL::TEXT)) AS c(object_type, relation, satisfying_relation)"
		if got != expect {
			t.Errorf("TypedValuesTable.SQL() with empty rows =\n%q\nwant:\n%q", got, expect)
		}
	})

	t.Run("empty rows no columns", func(t *testing.T) {
		table := TypedValuesTable{
			Rows:  nil,
			Alias: "x",
		}
		got := table.SQL()
		expect := "(VALUES (NULL::TEXT)) AS x"
		if got != expect {
			t.Errorf("TypedValuesTable.SQL() with no columns =\n%q\nwant:\n%q", got, expect)
		}
	})

	t.Run("without column names", func(t *testing.T) {
		table := TypedValuesTable{
			Rows:  []ValuesRow{{Lit("a"), Lit("b")}},
			Alias: "t",
		}
		got := table.SQL()
		expect := "(VALUES ('a', 'b')) AS t"
		if got != expect {
			t.Errorf("TypedValuesTable.SQL() without columns = %q, want %q", got, expect)
		}
	})

	t.Run("implements TableExpr", func(t *testing.T) {
		var table TableExpr = TypedValuesTable{
			Rows:    []ValuesRow{{Lit("test")}},
			Alias:   "t",
			Columns: []string{"col"},
		}
		if table.TableAlias() != "t" {
			t.Errorf("TableAlias() = %q, want %q", table.TableAlias(), "t")
		}
		if !strings.Contains(table.TableSQL(), "VALUES") {
			t.Errorf("TableSQL() should contain VALUES: %s", table.TableSQL())
		}
	})
}

func TestTypedClosureValuesTable(t *testing.T) {
	rows := []ValuesRow{
		{Lit("doc"), Lit("viewer"), Lit("editor")},
		{Lit("doc"), Lit("viewer"), Lit("viewer")},
	}
	table := TypedClosureValuesTable(rows, "c")

	if table.Alias != "c" {
		t.Errorf("Alias = %q, want %q", table.Alias, "c")
	}
	expectedCols := []string{"object_type", "relation", "satisfying_relation"}
	for i, col := range expectedCols {
		if i >= len(table.Columns) || table.Columns[i] != col {
			t.Errorf("Columns[%d] = %q, want %q", i, table.Columns[i], col)
		}
	}
	if len(table.Rows) != 2 {
		t.Errorf("Rows length = %d, want 2", len(table.Rows))
	}
}

func TestTypedUsersetValuesTable(t *testing.T) {
	rows := []ValuesRow{
		{Lit("doc"), Lit("viewer"), Lit("group"), Lit("member")},
	}
	table := TypedUsersetValuesTable(rows, "m")

	if table.Alias != "m" {
		t.Errorf("Alias = %q, want %q", table.Alias, "m")
	}
	expectedCols := []string{"object_type", "relation", "subject_type", "subject_relation"}
	for i, col := range expectedCols {
		if i >= len(table.Columns) || table.Columns[i] != col {
			t.Errorf("Columns[%d] = %q, want %q", i, table.Columns[i], col)
		}
	}
}

func TestBuildClosureTypedRows(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		rows := buildClosureTypedRows(nil)
		if rows != nil {
			t.Errorf("buildClosureTypedRows(nil) = %v, want nil", rows)
		}
	})

	t.Run("single row", func(t *testing.T) {
		input := []ClosureRow{
			{ObjectType: "doc", Relation: "viewer", SatisfyingRelation: "editor"},
		}
		rows := buildClosureTypedRows(input)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		got := rows[0].SQL()
		expect := "('doc', 'viewer', 'editor')"
		if got != expect {
			t.Errorf("rows[0].SQL() = %q, want %q", got, expect)
		}
	})

	t.Run("multiple rows sorted", func(t *testing.T) {
		input := []ClosureRow{
			{ObjectType: "doc", Relation: "viewer", SatisfyingRelation: "viewer"},
			{ObjectType: "doc", Relation: "editor", SatisfyingRelation: "editor"},
			{ObjectType: "doc", Relation: "viewer", SatisfyingRelation: "editor"},
		}
		rows := buildClosureTypedRows(input)
		if len(rows) != 3 {
			t.Fatalf("len(rows) = %d, want 3", len(rows))
		}
		// Verify sorted order: doc/editor/editor, doc/viewer/editor, doc/viewer/viewer
		expects := []string{
			"('doc', 'editor', 'editor')",
			"('doc', 'viewer', 'editor')",
			"('doc', 'viewer', 'viewer')",
		}
		for i, expect := range expects {
			got := rows[i].SQL()
			if got != expect {
				t.Errorf("rows[%d].SQL() = %q, want %q", i, got, expect)
			}
		}
	})

	t.Run("with special characters", func(t *testing.T) {
		input := []ClosureRow{
			{ObjectType: "it's", Relation: "a'test", SatisfyingRelation: "value"},
		}
		rows := buildClosureTypedRows(input)
		got := rows[0].SQL()
		if !strings.Contains(got, "''") {
			t.Errorf("Expected escaped quotes in %q", got)
		}
	})
}

func TestBuildUsersetTypedRows(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		rows := buildUsersetTypedRows(nil)
		if rows != nil {
			t.Errorf("buildUsersetTypedRows(nil) = %v, want nil", rows)
		}
	})

	t.Run("no userset patterns", func(t *testing.T) {
		input := []RelationAnalysis{
			{ObjectType: "doc", Relation: "viewer", UsersetPatterns: nil},
		}
		rows := buildUsersetTypedRows(input)
		if rows != nil {
			t.Errorf("buildUsersetTypedRows(empty patterns) = %v, want nil", rows)
		}
	})

	t.Run("single pattern", func(t *testing.T) {
		input := []RelationAnalysis{
			{
				ObjectType: "doc",
				Relation:   "viewer",
				UsersetPatterns: []UsersetPattern{
					{SubjectType: "group", SubjectRelation: "member"},
				},
			},
		}
		rows := buildUsersetTypedRows(input)
		if len(rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(rows))
		}
		got := rows[0].SQL()
		expect := "('doc', 'viewer', 'group', 'member')"
		if got != expect {
			t.Errorf("rows[0].SQL() = %q, want %q", got, expect)
		}
	})

	t.Run("deduplication", func(t *testing.T) {
		input := []RelationAnalysis{
			{
				ObjectType: "doc",
				Relation:   "viewer",
				UsersetPatterns: []UsersetPattern{
					{SubjectType: "group", SubjectRelation: "member"},
					{SubjectType: "group", SubjectRelation: "member"}, // duplicate
				},
			},
		}
		rows := buildUsersetTypedRows(input)
		if len(rows) != 1 {
			t.Errorf("len(rows) = %d, want 1 (deduped)", len(rows))
		}
	})

	t.Run("multiple patterns sorted", func(t *testing.T) {
		input := []RelationAnalysis{
			{
				ObjectType: "doc",
				Relation:   "viewer",
				UsersetPatterns: []UsersetPattern{
					{SubjectType: "team", SubjectRelation: "member"},
					{SubjectType: "group", SubjectRelation: "member"},
				},
			},
		}
		rows := buildUsersetTypedRows(input)
		if len(rows) != 2 {
			t.Fatalf("len(rows) = %d, want 2", len(rows))
		}
		// Should be sorted: group before team
		if !strings.Contains(rows[0].SQL(), "group") {
			t.Errorf("First row should contain 'group': %s", rows[0].SQL())
		}
		if !strings.Contains(rows[1].SQL(), "team") {
			t.Errorf("Second row should contain 'team': %s", rows[1].SQL())
		}
	})
}

func TestTypedValuesTableConsistencyWithStringBased(t *testing.T) {
	// Verify that typed and string-based VALUES produce equivalent SQL
	closureRows := []ClosureRow{
		{ObjectType: "doc", Relation: "viewer", SatisfyingRelation: "editor"},
		{ObjectType: "doc", Relation: "viewer", SatisfyingRelation: "viewer"},
	}

	// String-based (existing)
	stringValues := buildClosureValues(closureRows)
	stringTable := ClosureValuesTable(stringValues, "c")

	// Typed (new)
	typedRows := buildClosureTypedRows(closureRows)
	typedTable := TypedClosureValuesTable(typedRows, "c")

	// Both should produce the same SQL
	stringSQL := stringTable.SQL()
	typedSQL := typedTable.SQL()

	if stringSQL != typedSQL {
		t.Errorf("String-based and typed VALUES tables differ:\nString: %s\nTyped:  %s", stringSQL, typedSQL)
	}
}
