package openfgatests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/stretchr/testify/require"
)

// runExplainParityAssertions cross-references Explain against Check for
// every eligible CheckAssertion in the stage. For each assertion the
// dispatcher routes to a per-relation explain_* function; the returned
// Trace's Result must equal the assertion's Expectation. Drift here means
// Explain lies about the decision — the most load-bearing correctness
// invariant the Explain feature carries.
//
// Eligibility:
//   - assertion.ErrorCode == 0 (we are pinning success-path agreement, not
//     error parity which has its own runner block)
//   - len(assertion.ContextualTuples) == 0 (Explain doesn't accept
//     contextual tuples yet; mixing them would silently exercise a
//     different code path than Check)
//   - assertion.Tuple != nil
//
// Skipping (not failing):
//   - The dispatcher's no-entry sentinel. When the relation's renderer is
//     not yet eligible (see lib/sqlgen ComputeExplainEligibility) the
//     dispatcher returns a NodeUnion root labeled "explain not yet
//     supported …" with result=false. Asserting parity here would let the
//     sentinel falsely match every Check==false assertion as a "pass". We
//     log the skip so coverage gaps stay visible without poisoning the
//     run.
//
// Structural invariants are enforced even on skipped (sentinel) traces:
// the envelope must deserialise, Root must be non-nil, Result must be
// non-nil. Those are the contract the Trace type promises to the SDKs;
// breaking them would crash callers that don't even need the parity check.
func runExplainParityAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, checks []*CheckAssertion) {
	t.Helper()

	if client.openfgaBackend != nil {
		return // Explain is a melange-only API; the OpenFGA oracle has no equivalent.
	}

	eligible := explainParityEligible(checks)
	if len(eligible) == 0 {
		return
	}

	for i, a := range eligible {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("explain_parity_%d", i)
		} else {
			name = "explain_parity_" + name
		}

		t.Run(name, func(t *testing.T) {
			tk := a.Tuple
			req := &openfgav1.CheckRequest{
				StoreId:              storeID,
				AuthorizationModelId: modelID,
				TupleKey: &openfgav1.CheckRequestTupleKey{
					User:     tk.GetUser(),
					Relation: tk.GetRelation(),
					Object:   tk.GetObject(),
				},
			}
			trace, err := client.Explain(ctx, req)
			require.NoError(t, err,
				"explain failed for %s#%s on %s",
				tk.GetUser(), tk.GetRelation(), tk.GetObject())

			// Envelope structural invariants — must hold even for the
			// no-entry sentinel so downstream consumers can rely on the
			// shape regardless of feature support.
			require.NotNil(t, trace, "trace envelope must not be nil")
			require.NotNil(t, trace.Result, "trace.Result must be populated")
			require.NotNil(t, trace.Root, "trace.Root must not be nil")

			// Sentinel label means the dispatcher has no entry for this
			// (object_type, relation) — skip rather than assert, otherwise
			// the sentinel's hard-coded result=false would falsely "match"
			// every Check==false expectation as a pass.
			if label := trace.Root.Label; strings.Contains(label, "explain not yet supported") ||
				strings.Contains(label, "no relations defined") {
				t.Skipf("explain not supported for %s#%s: dispatcher sentinel",
					tk.GetObject(), tk.GetRelation())
			}

			require.Equal(t, a.Expectation, *trace.Result,
				"explain disagrees with check for %s#%s on %s (expectation=%v, got=%v)",
				tk.GetUser(), tk.GetRelation(), tk.GetObject(),
				a.Expectation, *trace.Result)
		})
	}
}

// explainParityEligible filters a stage's check assertions down to the
// subset Explain can faithfully reproduce today. The criteria are the
// load-bearing pieces:
//   - no error code (those are tested by the check error-parity path)
//   - no contextual tuples (Explain SQL has no p_contextual_tuples yet)
//   - non-nil tuple key
//
// Wildcard / userset subject filters are NOT excluded here — Explain
// supports both shapes via the userset-subject pre-check and wildcard
// sentinel, and the parity sweep is exactly where regressions in those
// paths should surface.
func explainParityEligible(checks []*CheckAssertion) []*CheckAssertion {
	out := make([]*CheckAssertion, 0, len(checks))
	for _, a := range checks {
		if a == nil || a.Tuple == nil {
			continue
		}
		if a.ErrorCode != 0 {
			continue
		}
		if len(a.ContextualTuples) != 0 {
			continue
		}
		out = append(out, a)
	}
	return out
}
