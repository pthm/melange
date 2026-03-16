package doctor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyTupleSignatures(t *testing.T) {
	validTypes := map[string]bool{"user": true, "organization": true, "repository": true}
	validRelations := map[string]map[string]bool{
		"organization": {"owner": true, "member": true},
		"repository":   {"admin": true, "reader": true, "org": true},
	}
	allowedSubjects := map[string]map[string]map[string]bool{
		"organization": {
			"owner":  {"user": true},
			"member": {"user": true},
		},
		"repository": {
			"admin":  {"user": true},
			"reader": {"user": true},
			"org":    {"organization": true},
		},
	}

	t.Run("all valid", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"repository", "admin", "user", 50},
			{"repository", "org", "organization", 30},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Empty(t, f.unknownSubjects)
		assert.Empty(t, f.invalidSubjects)
	})

	t.Run("unknown object type", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"widget", "viewer", "user", 42},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Contains(t, f.unknownObjectTypes[0], "widget")
		assert.Contains(t, f.unknownObjectTypes[0], "42")
		assert.Equal(t, int64(42), f.unknownObjectTypeCount)
	})

	t.Run("unknown relation", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"organization", "billing", "user", 15},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Len(t, f.unknownRelations, 1)
		assert.Contains(t, f.unknownRelations[0], "organization:billing")
		assert.Equal(t, int64(15), f.unknownRelationCount)
	})

	t.Run("unknown subject type", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "device", 30},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Len(t, f.unknownSubjects, 1)
		assert.Contains(t, f.unknownSubjects[0], "device")
		assert.Equal(t, int64(30), f.unknownSubjectCount)
	})

	t.Run("invalid subject type for relation", func(t *testing.T) {
		sigs := []tupleSignature{
			{"repository", "admin", "organization", 5},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Empty(t, f.unknownSubjects)
		assert.Len(t, f.invalidSubjects, 1)
		assert.Contains(t, f.invalidSubjects[0], "repository:admin")
		assert.Contains(t, f.invalidSubjects[0], "subject_type=organization")
		assert.Equal(t, int64(5), f.invalidSubjectCount)
	})

	t.Run("counts aggregate across signatures", func(t *testing.T) {
		sigs := []tupleSignature{
			{"widget", "viewer", "user", 10},
			{"widget", "editor", "user", 20},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Equal(t, int64(30), f.unknownObjectTypeCount)
		assert.Contains(t, f.unknownObjectTypes[0], "30")
	})

	t.Run("multiple categories at once", func(t *testing.T) {
		sigs := []tupleSignature{
			{"gadget", "view", "user", 5},              // unknown object type
			{"organization", "billing", "user", 10},    // unknown relation
			{"organization", "member", "device", 3},    // unknown subject type
			{"repository", "admin", "organization", 2}, // invalid subject type
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Len(t, f.unknownRelations, 1)
		assert.Len(t, f.unknownSubjects, 1)
		assert.Len(t, f.invalidSubjects, 1)
	})
}

func TestEmitTupleFindings(t *testing.T) {
	t.Run("all clean", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, StatusPass, report.Checks[0].Status)
		assert.Equal(t, "valid", report.Checks[0].Name)
	})

	t.Run("unknown object types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownObjectTypes:     []string{"widget (10 tuples)"},
			unknownObjectTypeCount: 10,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, StatusWarn, report.Checks[0].Status)
		assert.Equal(t, "unknown_object_types", report.Checks[0].Name)
		assert.Contains(t, report.Checks[0].Message, "10 tuples")
		assert.NotEmpty(t, report.Checks[0].FixHint)
	})

	t.Run("unknown relations only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownRelations:     []string{"org:billing (5 tuples)"},
			unknownRelationCount: 5,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "unknown_relations", report.Checks[0].Name)
	})

	t.Run("unknown subject types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownSubjects:     []string{"device (3 tuples)"},
			unknownSubjectCount: 3,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "unknown_subject_types", report.Checks[0].Name)
	})

	t.Run("invalid subject types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			invalidSubjects:     []string{"repo:admin subject_type=org (2 tuples)"},
			invalidSubjectCount: 2,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "invalid_subject_types", report.Checks[0].Name)
	})

	t.Run("all categories at once", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownObjectTypes:     []string{"a (1 tuples)"},
			unknownObjectTypeCount: 1,
			unknownRelations:       []string{"b:c (2 tuples)"},
			unknownRelationCount:   2,
			unknownSubjects:        []string{"d (3 tuples)"},
			unknownSubjectCount:    3,
			invalidSubjects:        []string{"e:f subject_type=g (4 tuples)"},
			invalidSubjectCount:    4,
		})
		assert.Len(t, report.Checks, 4, "should emit one check per category")
		assert.Equal(t, 0, report.Errors)
		assert.Equal(t, 4, report.Warnings)
	})
}

func TestTruncatedJoin(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}

	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 0), "maxItems=0 returns all")
	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 5), "exact count returns all")
	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 10), "over count returns all")
	assert.Equal(t, "a\nb\n... and 3 more", truncatedJoin(items, 2), "truncates with summary")
}
