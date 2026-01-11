package melange_test

import (
	"context"
	"testing"

	"github.com/pthm/melange/melange"
)

type testContextKey string

func TestDecisionContext(t *testing.T) {
	t.Run("DecisionUnset by default", func(t *testing.T) {
		ctx := context.Background()
		if got := melange.GetDecisionContext(ctx); got != melange.DecisionUnset {
			t.Errorf("GetDecision() = %v, want DecisionUnset", got)
		}
	})

	t.Run("WithDecision sets DecisionAllow", func(t *testing.T) {
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)
		if got := melange.GetDecisionContext(ctx); got != melange.DecisionAllow {
			t.Errorf("GetDecision() = %v, want DecisionAllow", got)
		}
	})

	t.Run("WithDecision sets DecisionDeny", func(t *testing.T) {
		ctx := melange.WithDecisionContext(context.Background(), melange.DecisionDeny)
		if got := melange.GetDecisionContext(ctx); got != melange.DecisionDeny {
			t.Errorf("GetDecision() = %v, want DecisionDeny", got)
		}
	})

	t.Run("child context inherits decision", func(t *testing.T) {
		parent := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)
		child := context.WithValue(parent, testContextKey("other"), "value")
		if got := melange.GetDecisionContext(child); got != melange.DecisionAllow {
			t.Errorf("GetDecision(child) = %v, want DecisionAllow", got)
		}
	})
}
