package melange

import (
	"context"
	"encoding/json"
	"fmt"
)

// Explain returns the resolution path the authorization engine walked when
// answering whether subject has relation on object. On success the trace
// shows the path that proved the permission; on failure it shows every
// attempted branch and where each one stopped — typically the most useful
// debugging output for "why doesn't X have Y?" questions.
//
// Explain does more work per call than Check (it constructs a JSONB trace
// rather than a boolean) and is intended for debugging and admin flows, not
// for the request-path permission decision. Use Check for hot-path
// authorization.
//
// # Stage 1 slice 1 scope
//
// The first slice of explain codegen covers relations whose access resolves
// through a single direct/implied tuple SELECT (the relation closure list
// inlined into the WHERE clause). Relations that require usersets, TTU,
// intersection, exclusion, or recursive function calls route through a
// "not yet supported" sentinel — the returned trace will have
// Result=false and a root label flagging the limitation. Future slices fill
// in those branches.
//
// # Option handling
//
// WithExplainMaxNodes is accepted but not yet enforced at the SQL layer in
// this codegen version; the generated functions don't truncate. The option
// is plumbed for forward compatibility — once truncation lands it will be
// honoured as `SET LOCAL melange.max_explain_nodes`. Setting it today is a
// no-op that does not error.
//
// # Validation
//
// Explain honours the same WithUsersetValidation / WithRequestValidation
// options as Check so the two APIs reject the same malformed inputs at the
// Go layer. Validation errors short-circuit before any SQL is issued; the
// returned (*Trace, error) is (nil, err).
//
// Returns nil and an error if validation, the dispatcher call, or JSON
// deserialisation fails.
func (c *Checker) Explain(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike, opts ...ExplainOption) (*Trace, error) {
	_ = applyExplain(opts) // see "Option handling" — limits land in a follow-up slice

	subj := subject.FGASubject()
	rel := relation.FGARelation()
	obj := object.FGAObject()

	if c.validateUserset {
		if err := c.validateUsersetSubject(ctx, c.q, subj); err != nil {
			return nil, err
		}
	}
	if c.validateRequest {
		if err := c.validateCheckRequest(ctx, c.q, subj, rel, obj); err != nil {
			return nil, err
		}
	}

	var raw []byte
	err := c.q.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)::text", prefixIdent("explain_permission", c.databaseSchema)),
		subj.Type, subj.ID, rel, obj.Type, obj.ID,
	).Scan(&raw)
	if err != nil {
		return nil, c.mapError("explain_permission", err)
	}

	var trace Trace
	if err := json.Unmarshal(raw, &trace); err != nil {
		return nil, fmt.Errorf("explain_permission: decoding trace: %w", err)
	}
	return &trace, nil
}
