package melange

import "context"

// Decision allows bypassing DB checks for admin tools and tests.
// Decisions provide explicit control over authorization behavior without
// modifying the underlying permission model or tuple data.
//
// The decision mechanism has two layers:
//  1. Checker-level: Set via WithDecision() at Checker construction
//  2. Context-level: Set via WithDecisionContext() and opt-in via WithContextDecision()
//
// Context-based decisions are opt-in by design. Applications must explicitly
// enable WithContextDecision() when creating the Checker to prevent accidental
// authorization bypasses from propagating through middleware. This makes the
// security boundary explicit: "this Checker respects context overrides."
type Decision int

// decisionContextKey is a custom type for context keys to avoid collisions.
type decisionContextKey struct{}

var decisionKey = decisionContextKey{}

const (
	// DecisionUnset means no override - perform normal permission check.
	DecisionUnset Decision = iota

	// DecisionAllow bypasses checks and always returns true (allowed).
	// Use for admin tools, background jobs, or testing authorized code paths.
	DecisionAllow

	// DecisionDeny bypasses checks and always returns false (denied).
	// Use for testing unauthorized code paths without database setup.
	DecisionDeny
)

// WithDecisionContext returns a new context with the given decision.
// This allows decision overrides to flow through context rather than
// requiring explicit Checker construction.
//
// IMPORTANT: The Checker does NOT automatically consult this context value.
// Applications must opt-in via WithContextDecision() when creating the Checker.
// This prevents accidental authorization bypasses from middleware.
//
// Prefer WithDecision option for explicit control. Use context-based decisions
// when the override needs to propagate through multiple layers where passing
// a Checker instance is impractical (e.g., testing frameworks, admin mode).
func WithDecisionContext(ctx context.Context, decision Decision) context.Context {
	return context.WithValue(ctx, decisionKey, decision)
}

// GetDecisionContext retrieves the decision from context.
// Returns DecisionUnset if no decision is set.
//
// Applications can use this to check for decision overrides before creating
// a Checker, enabling context-based bypass patterns.
func GetDecisionContext(ctx context.Context) Decision {
	if decision, ok := ctx.Value(decisionKey).(Decision); ok {
		return decision
	}
	return DecisionUnset
}
