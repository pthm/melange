package melange_test

import (
	"testing"

	"github.com/pthm/melange"
)

func TestListObjects_DecisionDeny(t *testing.T) {
	// Create checker with DecisionDeny
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

	ctx := t.Context()

	ids, err := checker.ListObjects(ctx, testSubject{}, testRelation{}, "test")
	if err != nil {
		t.Errorf("ListObjects error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListObjects should return nil for DecisionDeny, got %v", ids)
	}
}

func TestListSubjects_DecisionDeny(t *testing.T) {
	// Create checker with DecisionDeny
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

	ctx := t.Context()

	ids, err := checker.ListSubjects(ctx, testObject{}, testRelation{}, "user")
	if err != nil {
		t.Errorf("ListSubjects error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListSubjects should return nil for DecisionDeny, got %v", ids)
	}
}

func TestListOperations_ContextDecision(t *testing.T) {
	// Create checker with context decision enabled
	checker := melange.NewChecker(nil, melange.WithContextDecision())

	ctx := melange.WithDecisionContext(t.Context(), melange.DecisionDeny)

	ids, err := checker.ListObjects(ctx, testSubject{}, testRelation{}, "test")
	if err != nil {
		t.Errorf("ListObjects error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListObjects should return nil for context DecisionDeny, got %v", ids)
	}

	ids, err = checker.ListSubjects(ctx, testObject{}, testRelation{}, "user")
	if err != nil {
		t.Errorf("ListSubjects error: %v", err)
	}
	if ids != nil {
		t.Errorf("ListSubjects should return nil for context DecisionDeny, got %v", ids)
	}
}

// Test helpers to satisfy SubjectLike, ObjectLike, and RelationLike interfaces

type testSubject struct{}

func (t testSubject) FGASubject() melange.Object {
	return melange.Object{Type: "user", ID: "test"}
}

type testObject struct{}

func (t testObject) FGAObject() melange.Object {
	return melange.Object{Type: "repository", ID: "test"}
}

type testRelation struct{}

func (t testRelation) FGARelation() melange.Relation {
	return "can_read"
}
