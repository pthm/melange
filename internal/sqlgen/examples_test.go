package sqlgen_test

import (
	"strings"
	"testing"

	"github.com/pthm/melange/internal/sqlgen"
)

// This file demonstrates the DSL query patterns.
// Each test shows how to construct SQL queries using the DSL types.

// TestListObjectsDirectQuery shows the DSL equivalent of ListObjectsDirectQuery.
// This is the simplest query pattern - direct tuple lookup.
//
// Previous implementation (for reference):
//
//	where := []bob.Expression{
//	    psql.Quote("t", "object_type").EQ(psql.S(input.ObjectType)),
//	    psql.Quote("t", "relation").In(literalList(input.Relations)...),
//	    psql.Quote("t", "subject_type").EQ(param("p_subject_type")),
//	    param("p_subject_type").In(literalList(input.AllowedSubjectTypes)...),
//	    subjectIDCheckExpr(psql.Quote("t", "subject_id"), input.AllowWildcard),
//	}
//	query := psql.Select(
//	    sm.Columns(psql.Raw("t.object_id")),
//	    sm.From("melange_tuples").As("t"),
//	    sm.Where(psql.And(where...)),
//	    sm.Distinct(),
//	)
func TestListObjectsDirectQuery(t *testing.T) {
	// DSL version
	q := sqlgen.Tuples("t").
		ObjectType("document").
		Relations("viewer", "editor").
		WhereSubjectType(sqlgen.SubjectType).
		Where(sqlgen.In{Expr: sqlgen.SubjectType, Values: []string{"user", "group"}}).
		WhereSubjectID(sqlgen.SubjectID, true).
		Select("t.object_id").
		Distinct()

	sql := q.SQL()

	// Verify key parts are present
	checks := []string{
		"SELECT DISTINCT t.object_id",
		"FROM melange_tuples AS t",
		"t.object_type = 'document'",
		"t.relation IN ('viewer', 'editor')",
		"t.subject_type = p_subject_type",
		"p_subject_type IN ('user', 'group')",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

// TestDirectCheck shows the DSL equivalent of DirectCheck (EXISTS pattern).
//
// Previous implementation (for reference):
//
//	query := psql.Select(
//	    sm.Columns(psql.Raw("1")),
//	    sm.From("melange_tuples"),
//	    sm.Where(where),
//	    sm.Limit(1),
//	)
//	return existsSQL(query)
func TestDirectCheck(t *testing.T) {
	// DSL version
	q := sqlgen.Tuples("").
		ObjectType("document").
		Relations("viewer", "editor").
		Where(
			sqlgen.Eq{sqlgen.Col{Column: "object_id"}, sqlgen.ObjectID},
			sqlgen.In{Expr: sqlgen.Col{Column: "subject_type"}, Values: []string{"user"}},
			sqlgen.Eq{sqlgen.Col{Column: "subject_type"}, sqlgen.SubjectType},
			sqlgen.SubjectIDMatch(sqlgen.Col{Column: "subject_id"}, sqlgen.SubjectID, false),
		).
		Select("1").
		Limit(1)

	existsSQL := q.ExistsSQL()

	if !strings.HasPrefix(existsSQL, "EXISTS (") {
		t.Errorf("Expected EXISTS wrapper, got: %s", existsSQL)
	}
	if !strings.Contains(existsSQL, "LIMIT 1") {
		t.Errorf("SQL missing LIMIT 1:\n%s", existsSQL)
	}
}

// TestUsersetCheck shows the DSL equivalent of UsersetCheck (JOIN pattern).
//
// Previous implementation (for reference):
//
//	joinConditions := []bob.Expression{
//	    psql.Quote("membership", "object_type").EQ(psql.S(input.SubjectType)),
//	    psql.Quote("membership", "object_id").EQ(psql.Raw("split_part(grant_tuple.subject_id, '#', 1)")),
//	    psql.Quote("membership", "relation").In(relExprs...),
//	    psql.Quote("membership", "subject_type").EQ(param("p_subject_type")),
//	    subjectIDCheckExpr(psql.Quote("membership", "subject_id"), input.AllowWildcard),
//	}
//	query := psql.Select(
//	    sm.Columns(psql.Raw("1")),
//	    sm.From("melange_tuples").As("grant_tuple"),
//	    sm.InnerJoin("melange_tuples").As("membership").On(joinConditions...),
//	    sm.Where(where),
//	    sm.Limit(1),
//	)
func TestUsersetCheck(t *testing.T) {
	subjectType := "group"
	subjectRelation := "member"
	satisfyingRelations := []string{"member", "admin"}

	// DSL version
	q := sqlgen.Tuples("grant_tuple").
		ObjectType("document").
		Relations("viewer").
		Where(
			sqlgen.Eq{sqlgen.Col{Table: "grant_tuple", Column: "object_id"}, sqlgen.ObjectID},
			sqlgen.Eq{sqlgen.Col{Table: "grant_tuple", Column: "subject_type"}, sqlgen.Lit(subjectType)},
			sqlgen.HasUserset{sqlgen.Col{Table: "grant_tuple", Column: "subject_id"}},
			sqlgen.Eq{
				sqlgen.UsersetRelation{sqlgen.Col{Table: "grant_tuple", Column: "subject_id"}},
				sqlgen.Lit(subjectRelation),
			},
		).
		JoinTuples("membership",
			sqlgen.Eq{sqlgen.Col{Table: "membership", Column: "object_type"}, sqlgen.Lit(subjectType)},
			sqlgen.Eq{
				sqlgen.Col{Table: "membership", Column: "object_id"},
				sqlgen.UsersetObjectID{sqlgen.Col{Table: "grant_tuple", Column: "subject_id"}},
			},
			sqlgen.In{Expr: sqlgen.Col{Table: "membership", Column: "relation"}, Values: satisfyingRelations},
			sqlgen.Eq{sqlgen.Col{Table: "membership", Column: "subject_type"}, sqlgen.SubjectType},
			sqlgen.SubjectIDMatch(sqlgen.Col{Table: "membership", Column: "subject_id"}, sqlgen.SubjectID, true),
		).
		Select("1").
		Limit(1)

	sql := q.ExistsSQL()

	// Verify key parts
	checks := []string{
		"FROM melange_tuples AS grant_tuple",
		"INNER JOIN melange_tuples AS membership ON",
		"split_part(grant_tuple.subject_id, '#', 1)",
		"split_part(grant_tuple.subject_id, '#', 2) = 'member'",
		"position('#' in grant_tuple.subject_id) > 0",
		"membership.relation IN ('member', 'admin')",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

// TestExclusionPattern shows the DSL equivalent of simple exclusion.
//
// Previous implementation (for reference):
//
//	subquery := psql.Select(
//	    sm.Columns(psql.Raw("1")),
//	    sm.From("melange_tuples").As("excl"),
//	    sm.Where(psql.And(
//	        psql.Quote("excl", "object_type").EQ(psql.S(input.ObjectType)),
//	        psql.Quote("excl", "object_id").EQ(objectIDExpr),
//	        psql.Quote("excl", "relation").EQ(psql.S(rel)),
//	        psql.Quote("excl", "subject_type").EQ(subjectTypeExpr),
//	        psql.Or(
//	            psql.Quote("excl", "subject_id").EQ(subjectIDExpr),
//	            psql.Quote("excl", "subject_id").EQ(psql.S("*")),
//	        ),
//	    )),
//	)
//	notExists, _ := notExistsExpr(subquery)
func TestExclusionPattern(t *testing.T) {
	objectIDExpr := sqlgen.Col{Table: "t", Column: "object_id"}

	// DSL version - using SimpleExclusion helper
	excl := sqlgen.SimpleExclusion("document", "blocked", objectIDExpr, sqlgen.SubjectType, sqlgen.SubjectID)

	sql := excl.SQL()

	if !strings.HasPrefix(sql, "NOT EXISTS (") {
		t.Errorf("Expected NOT EXISTS wrapper, got: %s", sql)
	}

	checks := []string{
		"excl.object_type = 'document'",
		"excl.relation IN ('blocked')",
		"excl.object_id = t.object_id",
		"excl.subject_type = p_subject_type",
		"excl.subject_id = '*'",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

// TestCheckPermissionInternal shows the DSL equivalent of CheckPermissionInternalExpr.
//
// Previous implementation (for reference):
//
//	psql.Raw(fmt.Sprintf(
//	    "check_permission_internal(%s, %s, '%s', %s, %s, ARRAY[]::TEXT[]) = %s",
//	    subjectTypeExpr, subjectIDExpr, relation, objectTypeExpr, objectIDExpr, result,
//	))
func TestCheckPermissionInternal(t *testing.T) {
	// DSL version
	check := sqlgen.CheckPermission{
		Subject:  sqlgen.SubjectParams(),
		Relation: "viewer",
		Object: sqlgen.ObjectRef{
			Type: sqlgen.Lit("document"),
			ID:   sqlgen.Col{Table: "t", Column: "object_id"},
		},
		ExpectAllow: true,
	}

	sql := check.SQL()
	expect := "check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'document', t.object_id, ARRAY[]::TEXT[]) = 1"
	if sql != expect {
		t.Errorf("SQL = %q\nwant: %q", sql, expect)
	}

	// Test expect deny
	check.ExpectAllow = false
	sql = check.SQL()
	if !strings.HasSuffix(sql, "= 0") {
		t.Errorf("Expected '= 0' suffix for deny, got: %s", sql)
	}
}

// TestListObjectsUsersetPatternSimple shows a complex query with JOIN and userset patterns.
func TestListObjectsUsersetPatternSimple(t *testing.T) {
	objectType := "document"
	subjectType := "group"
	subjectRelation := "member"
	sourceRelations := []string{"viewer"}
	satisfyingRelations := []string{"member", "admin"}
	allowedSubjectTypes := []string{"user"}
	allowWildcard := true

	// DSL version
	q := sqlgen.Tuples("t").
		ObjectType(objectType).
		Relations(sourceRelations...).
		Where(
			sqlgen.Eq{sqlgen.Col{Table: "t", Column: "subject_type"}, sqlgen.Lit(subjectType)},
			sqlgen.HasUserset{sqlgen.Col{Table: "t", Column: "subject_id"}},
			sqlgen.Eq{
				sqlgen.UsersetRelation{sqlgen.Col{Table: "t", Column: "subject_id"}},
				sqlgen.Lit(subjectRelation),
			},
		).
		JoinTuples("m",
			sqlgen.Eq{sqlgen.Col{Table: "m", Column: "object_type"}, sqlgen.Lit(subjectType)},
			sqlgen.Eq{
				sqlgen.Col{Table: "m", Column: "object_id"},
				sqlgen.UsersetObjectID{sqlgen.Col{Table: "t", Column: "subject_id"}},
			},
			sqlgen.In{Expr: sqlgen.Col{Table: "m", Column: "relation"}, Values: satisfyingRelations},
			sqlgen.Eq{sqlgen.Col{Table: "m", Column: "subject_type"}, sqlgen.SubjectType},
			sqlgen.In{Expr: sqlgen.SubjectType, Values: allowedSubjectTypes},
			sqlgen.SubjectIDMatch(sqlgen.Col{Table: "m", Column: "subject_id"}, sqlgen.SubjectID, allowWildcard),
		).
		Select("t.object_id").
		Distinct()

	sql := q.SQL()

	// Verify complex query structure
	checks := []string{
		"SELECT DISTINCT t.object_id",
		"FROM melange_tuples AS t",
		"INNER JOIN melange_tuples AS m ON",
		"t.subject_type = 'group'",
		"position('#' in t.subject_id) > 0",
		"split_part(t.subject_id, '#', 2) = 'member'",
		"m.object_type = 'group'",
		"split_part(t.subject_id, '#', 1)",
		"m.relation IN ('member', 'admin')",
		"m.subject_type = p_subject_type",
		"p_subject_type IN ('user')",
	}
	for _, check := range checks {
		if !strings.Contains(sql, check) {
			t.Errorf("SQL missing %q:\n%s", check, sql)
		}
	}
}

// TestComplexExclusions shows the ExclusionConfig builder.
func TestComplexExclusions(t *testing.T) {
	config := sqlgen.ExclusionConfig{
		ObjectType:               "document",
		ObjectIDExpr:             sqlgen.Col{Table: "t", Column: "object_id"},
		SubjectTypeExpr:          sqlgen.SubjectType,
		SubjectIDExpr:            sqlgen.SubjectID,
		SimpleExcludedRelations:  []string{"blocked"},
		ComplexExcludedRelations: []string{"banned"},
	}

	predicates := config.BuildPredicates()

	if len(predicates) != 2 {
		t.Fatalf("Expected 2 predicates, got %d", len(predicates))
	}

	// First predicate: NOT EXISTS for simple exclusion
	simpleSQL := predicates[0].SQL()
	if !strings.HasPrefix(simpleSQL, "NOT EXISTS (") {
		t.Errorf("Expected NOT EXISTS for simple exclusion, got: %s", simpleSQL)
	}

	// Second predicate: check_permission_internal = 0 for complex exclusion
	complexSQL := predicates[1].SQL()
	if !strings.Contains(complexSQL, "check_permission_internal") || !strings.HasSuffix(complexSQL, "= 0") {
		t.Errorf("Expected check_permission_internal = 0 for complex exclusion, got: %s", complexSQL)
	}
}
