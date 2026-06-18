package melange

// ExplainOption configures a single Checker.Explain call. Options resolve
// either as direct SQL parameters to the explain dispatcher or as
// `SET LOCAL melange.*` statements at request start; both paths honour the
// three-tier priority: per-call > session GUC > built-in default.

// ExplainOption configures a single Checker.Explain call.
type ExplainOption func(*explainOpts)

// explainOpts holds resolved Explain options. The zero value means
// "use server-side defaults" (i.e. no SET LOCAL statements are emitted).
type explainOpts struct {
	maxNodes int // 0 => unset
}

// applyExplain runs each option against a fresh explainOpts and returns it.
func applyExplain(options []ExplainOption) explainOpts {
	var o explainOpts
	for _, opt := range options {
		opt(&o)
	}
	return o
}

// WithExplainMaxNodes is reserved for the Explain node-count cap. It is
// plumbed through the option pattern for forward compatibility, but the
// generated explain_* functions in this codegen version do not yet enforce
// it — setting it is currently a no-op. A later codegen slice will honour
// the value via the `melange.max_explain_nodes` GUC with the built-in
// default (100). Pass <= 0 to mean "unset".
func WithExplainMaxNodes(n int) ExplainOption {
	return func(o *explainOpts) {
		if n > 0 {
			o.maxNodes = n
		}
	}
}
