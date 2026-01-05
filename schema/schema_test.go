package schema_test

import (
	"testing"

	"github.com/pthm/melange/schema"
)

func TestSubjectTypes(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "user",
			Relations: []schema.RelationDefinition{
				{Name: "self", SubjectTypes: []string{"user"}},
			},
		},
		{
			Name: "organization",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "member", SubjectTypes: []string{"user", "team"}},
			},
		},
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "org", SubjectTypes: []string{"organization"}},
				{Name: "public", SubjectTypes: []string{"user:*"}}, // wildcard
				{Name: "can_read", ImpliedBy: []string{"owner"}},   // no direct subjects
			},
		},
	}

	subjects := schema.SubjectTypes(types)

	// Should contain: user, team, organization (user:* becomes user)
	expected := map[string]bool{
		"user":         true,
		"team":         true,
		"organization": true,
	}

	if len(subjects) != len(expected) {
		t.Errorf("SubjectTypes returned %d types, want %d", len(subjects), len(expected))
	}

	for _, s := range subjects {
		if !expected[s] {
			t.Errorf("unexpected subject type: %s", s)
		}
	}
}

func TestRelationSubjects(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "collaborator", SubjectTypes: []string{"user", "team"}},
				{Name: "public", SubjectTypes: []string{"user:*"}},
				{Name: "can_read", ImpliedBy: []string{"owner"}}, // no direct subjects
			},
		},
	}

	t.Run("single subject type", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "repository", "owner")
		if len(subjects) != 1 || subjects[0] != "user" {
			t.Errorf("RelationSubjects = %v, want [user]", subjects)
		}
	})

	t.Run("multiple subject types", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "repository", "collaborator")
		if len(subjects) != 2 {
			t.Errorf("RelationSubjects = %v, want [user, team]", subjects)
		}
	})

	t.Run("wildcard stripped", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "repository", "public")
		if len(subjects) != 1 || subjects[0] != "user" {
			t.Errorf("RelationSubjects = %v, want [user]", subjects)
		}
	})

	t.Run("no direct subjects", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "repository", "can_read")
		if subjects != nil {
			t.Errorf("RelationSubjects = %v, want nil", subjects)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "unknown", "owner")
		if subjects != nil {
			t.Errorf("RelationSubjects = %v, want nil", subjects)
		}
	})

	t.Run("unknown relation", func(t *testing.T) {
		subjects := schema.RelationSubjects(types, "repository", "unknown")
		if subjects != nil {
			t.Errorf("RelationSubjects = %v, want nil", subjects)
		}
	})
}

func TestToAuthzModels(t *testing.T) {
	types := []schema.TypeDefinition{
		{
			Name: "repository",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "admin", SubjectTypes: []string{"user"}, ImpliedBy: []string{"owner"}},
				{
					Name:           "can_read",
					ParentRelation: "can_read",
					ParentType:     "org",
				},
			},
		},
	}

	models := schema.ToAuthzModels(types)

	t.Run("direct subject type", func(t *testing.T) {
		// Should have entry for repository.owner with subject_type=user
		found := false
		for _, m := range models {
			if m.ObjectType == "repository" && m.Relation == "owner" && m.SubjectType != nil && *m.SubjectType == "user" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing direct subject type entry for repository.owner")
		}
	})

	t.Run("implied by entry", func(t *testing.T) {
		// Should have entry for repository.admin implied by owner
		found := false
		for _, m := range models {
			if m.ObjectType == "repository" && m.Relation == "admin" && m.ImpliedBy != nil && *m.ImpliedBy == "owner" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing implied_by entry for repository.admin")
		}
	})

	t.Run("parent relation with linking relation", func(t *testing.T) {
		// Should have entry for repository.can_read with parent_relation=can_read and subject_type=org (linking relation)
		found := false
		for _, m := range models {
			if m.ObjectType == "repository" && m.Relation == "can_read" &&
				m.ParentRelation != nil && *m.ParentRelation == "can_read" &&
				m.SubjectType != nil && *m.SubjectType == "org" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing parent relation entry for repository.can_read (should have subject_type='org' as linking relation)")
		}
	})
}

func TestToAuthzModels_TransitiveClosure(t *testing.T) {
	// Test: owner -> admin -> member
	types := []schema.TypeDefinition{
		{
			Name: "org",
			Relations: []schema.RelationDefinition{
				{Name: "owner", SubjectTypes: []string{"user"}},
				{Name: "admin", ImpliedBy: []string{"owner"}},
				{Name: "member", ImpliedBy: []string{"admin"}},
			},
		},
	}

	models := schema.ToAuthzModels(types)

	// member should have implied_by for both admin and owner (transitive)
	adminImplied := false
	ownerImplied := false
	for _, m := range models {
		if m.ObjectType == "org" && m.Relation == "member" && m.ImpliedBy != nil {
			if *m.ImpliedBy == "admin" {
				adminImplied = true
			}
			if *m.ImpliedBy == "owner" {
				ownerImplied = true
			}
		}
	}

	if !adminImplied {
		t.Error("member should have implied_by entry for admin")
	}
	if !ownerImplied {
		t.Error("member should have implied_by entry for owner (transitive)")
	}
}
