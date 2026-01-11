package melange_test

import (
	"context"
	"testing"

	"github.com/pthm/melange/melange"
)

// TestListSubjects_DecisionOverrides tests ListSubjects behavior with decision overrides.
// Without a database, we can only test the decision override paths.
func TestListSubjects_DecisionOverrides(t *testing.T) {
	ctx := context.Background()

	t.Run("DecisionDeny returns empty list", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
		object := melange.Object{Type: "repository", ID: "123"}

		ids, err := checker.ListSubjects(ctx, object, melange.Relation("can_read"), "user")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ids != nil {
			t.Errorf("ListSubjects should return nil for DecisionDeny, got %v", ids)
		}
	})

	t.Run("context DecisionDeny returns empty list", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithContextDecision())
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionDeny)
		object := melange.Object{Type: "repository", ID: "123"}

		ids, err := checker.ListSubjects(ctx, object, melange.Relation("can_read"), "user")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ids != nil {
			t.Errorf("ListSubjects should return nil for context DecisionDeny, got %v", ids)
		}
	})
}

// TestListObjects_DecisionOverrides tests ListObjects behavior with decision overrides.
func TestListObjects_DecisionOverrides(t *testing.T) {
	ctx := context.Background()

	t.Run("DecisionDeny returns empty list", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
		subject := melange.Object{Type: "user", ID: "123"}

		ids, err := checker.ListObjects(ctx, subject, melange.Relation("can_read"), "repository")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ids != nil {
			t.Errorf("ListObjects should return nil for DecisionDeny, got %v", ids)
		}
	})

	t.Run("context DecisionDeny returns empty list", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithContextDecision())
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionDeny)
		subject := melange.Object{Type: "user", ID: "123"}

		ids, err := checker.ListObjects(ctx, subject, melange.Relation("can_read"), "repository")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ids != nil {
			t.Errorf("ListObjects should return nil for context DecisionDeny, got %v", ids)
		}
	})
}

// TestCheck_DecisionOverrides tests Check behavior with decision overrides.
func TestCheck_DecisionOverrides(t *testing.T) {
	ctx := context.Background()
	subject := melange.Object{Type: "user", ID: "123"}
	object := melange.Object{Type: "repository", ID: "456"}

	t.Run("DecisionAllow bypasses database", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

		ok, err := checker.Check(ctx, subject, melange.Relation("can_read"), object)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("Check should return true for DecisionAllow")
		}
	})

	t.Run("DecisionDeny bypasses database", func(t *testing.T) {
		checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

		ok, err := checker.Check(ctx, subject, melange.Relation("can_read"), object)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ok {
			t.Error("Check should return false for DecisionDeny")
		}
	})

	t.Run("context decision takes precedence", func(t *testing.T) {
		// Checker has DecisionDeny, but context has DecisionAllow
		checker := melange.NewChecker(nil,
			melange.WithDecision(melange.DecisionDeny),
			melange.WithContextDecision(),
		)
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)

		ok, err := checker.Check(ctx, subject, melange.Relation("can_read"), object)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("Check should return true when context DecisionAllow overrides")
		}
	})

	t.Run("context decision opt-in required", func(t *testing.T) {
		// Without WithContextDecision, context decisions are ignored
		checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)

		ok, err := checker.Check(ctx, subject, melange.Relation("can_read"), object)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ok {
			t.Error("Check should ignore context decision without WithContextDecision")
		}
	})
}
