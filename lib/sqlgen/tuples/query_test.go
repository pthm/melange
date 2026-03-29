package tuples

import (
	"strings"
	"testing"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

func TestTuples_BasicQuery(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("document").
		Relations("viewer").
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "FROM melange_tuples AS t")
	assertContains(t, sql, "t.object_type = 'document'")
	assertContains(t, sql, "t.relation IN ('viewer')")
	assertContains(t, sql, "t.object_id")
}

func TestTuples_Alias(t *testing.T) {
	q := Tuples("", "myalias")
	if got := q.Alias(); got != "myalias" {
		t.Errorf("Alias() = %q, want %q", got, "myalias")
	}
}

func TestTuples_MultipleRelations(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		Relations("viewer", "editor", "owner").
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "t.relation IN ('viewer', 'editor', 'owner')")
}

func TestTuples_Distinct(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		SelectCol("object_id").
		Distinct().
		SQL()

	assertContains(t, sql, "SELECT DISTINCT")
}

func TestTuples_WhereSubjectType(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereSubjectType(sqldsl.Lit("user")).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "t.subject_type = 'user'")
}

func TestTuples_WhereSubjectTypeIn(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereSubjectTypeIn("user", "group").
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "t.subject_type IN ('user', 'group')")
}

func TestTuples_WhereSubject(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereSubject(sqldsl.SubjectRef{
			Type: sqldsl.Lit("user"),
			ID:   sqldsl.Lit("alice"),
		}).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "t.subject_type = 'user'")
	assertContains(t, sql, "t.subject_id = 'alice'")
}

func TestTuples_WhereSubjectID(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereSubjectID(sqldsl.Lit("alice"), false).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "t.subject_id = 'alice'")
}

func TestTuples_WhereObject(t *testing.T) {
	sql := Tuples("", "t").
		WhereObject(sqldsl.ObjectRef{
			Type: sqldsl.Lit("document"),
			ID:   sqldsl.Lit("doc1"),
		}).
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "t.object_type = 'document'")
	assertContains(t, sql, "t.object_id = 'doc1'")
}

func TestTuples_WhereObjectID(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereObjectID(sqldsl.Lit("doc1")).
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "t.object_id = 'doc1'")
}

func TestTuples_WhereHasUserset(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereHasUserset().
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "position('#' in t.subject_id) > 0")
}

func TestTuples_WhereNoUserset(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereNoUserset().
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "position('#' in t.subject_id) = 0")
}

func TestTuples_WhereUsersetRelation(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereUsersetRelation("member").
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "split_part(t.subject_id, '#', 2) = 'member'")
}

func TestTuples_WhereUsersetRelationLike(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		WhereUsersetRelationLike("member").
		SelectCol("subject_id").
		SQL()

	assertContains(t, sql, "t.subject_id LIKE '%#member'")
}

func TestTuples_WhereNilSkipped(t *testing.T) {
	q := Tuples("", "t").ObjectType("doc").SelectCol("object_id")
	q.Where(nil, sqldsl.Lit("true"), nil)
	sql := q.SQL()

	// Should have 'true' but not crash from nil
	assertContains(t, sql, "true")
}

func TestTuples_JoinTuples(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		JoinTuples("m",
			sqldsl.Eq{Left: sqldsl.Col{Table: "m", Column: "object_id"}, Right: sqldsl.Lit("x")},
		).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "INNER JOIN melange_tuples AS m ON")
	assertContains(t, sql, "m.object_id = 'x'")
}

func TestTuples_LeftJoin(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		LeftJoin("other_table", "o",
			sqldsl.Eq{Left: sqldsl.Col{Table: "o", Column: "id"}, Right: sqldsl.Col{Table: "t", Column: "object_id"}},
		).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "LEFT JOIN other_table AS o ON")
}

func TestTuples_SelectExpr(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		SelectExpr(sqldsl.Col{Table: "t", Column: "subject_id"}).
		SQL()

	assertContains(t, sql, "t.subject_id")
}

func TestTuples_Select(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		Select("t.object_id", "t.subject_id").
		SQL()

	assertContains(t, sql, "t.object_id, t.subject_id")
}

func TestTuples_Limit(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		SelectCol("object_id").
		Limit(10).
		SQL()

	assertContains(t, sql, "LIMIT 10")
}

func TestTuples_ExistsSQL(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		SelectCol("object_id").
		ExistsSQL()

	if !strings.HasPrefix(sql, "EXISTS (") {
		t.Errorf("ExistsSQL() should start with 'EXISTS (', got: %s", sql)
	}
}

func TestTuples_NotExistsSQL(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		SelectCol("object_id").
		NotExistsSQL()

	if !strings.HasPrefix(sql, "NOT EXISTS (") {
		t.Errorf("NotExistsSQL() should start with 'NOT EXISTS (', got: %s", sql)
	}
}

func TestTuples_Build_NoObjectType(t *testing.T) {
	// When no object type is set, WHERE should not include it
	sql := Tuples("", "t").SelectCol("object_id").SQL()
	if strings.Contains(sql, "object_type") {
		t.Error("SQL should not contain object_type filter when none set")
	}
}

func TestTuples_Build_NoRelations(t *testing.T) {
	sql := Tuples("", "t").ObjectType("doc").SelectCol("object_id").SQL()
	if strings.Contains(sql, "relation IN") {
		t.Error("SQL should not contain relation IN filter when none set")
	}
}

func TestTuples_JoinRaw(t *testing.T) {
	sql := Tuples("", "t").
		ObjectType("doc").
		JoinRaw("CROSS JOIN LATERAL", "some_function('x') AS f",
			sqldsl.Eq{Left: sqldsl.Col{Table: "f", Column: "id"}, Right: sqldsl.Col{Table: "t", Column: "object_id"}},
		).
		SelectCol("object_id").
		SQL()

	assertContains(t, sql, "CROSS JOIN LATERAL")
	assertContains(t, sql, "some_function('x') AS f")
}

func assertContains(t *testing.T, sql, substr string) {
	t.Helper()
	if !strings.Contains(sql, substr) {
		t.Errorf("SQL should contain %q, got:\n%s", substr, sql)
	}
}
