package melange

import "context"

// Decision allows bypassing DB checks for admin tools and tests.
// Decisions are set at Checker construction time via WithDecision, making
// the bypass explicit and visible in code.
type Decision int

const decisionKey = "melange_decision"

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
// Prefer WithDecision option for explicit control. Use context-based decisions
// when the override needs to propagate through multiple layers where passing
// a Checker instance is impractical.
//
// Note: The Checker does NOT automatically consult this context value. This is
// a utility for applications that want to propagate authorization decisions
// through their own middleware or handler chains.
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
