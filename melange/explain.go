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
// # Coverage
//
// Explain agrees with Check across direct grants, implied relations, userset
// references, TTU (`relation from parent`), intersection (`a and b`), and
// exclusion (`a but not b`). Relations without a generated explain function
// return the dispatcher's no-entry sentinel (result=false, with a label
// identifying the pair as unsupported).
//
// # Truncation
//
// When the schema can produce a large trace, pass WithExplainMaxNodes(n)
// to cap the response. The cap is also honorable as a session GUC:
// `SET melange.max_explain_nodes = N;` then plain Explain calls inherit
// the limit. Both the per-call argument and the session GUC override the
// built-in default (100). On truncation the returned Trace has
// `Truncated == true` and ends in a NodeTruncated subtree where the
// budget ran out.
//
// # Validation
//
// Explain honors the same WithUsersetValidation / WithRequestValidation
// options as Check so the two APIs reject the same malformed inputs at the
// Go layer. Validation errors short-circuit before any SQL is issued; the
// returned (*Trace, error) is (nil, err).
//
// Returns nil and an error if validation, the dispatcher call, or JSON
// deserialisation fails.
func (c *Checker) Explain(ctx context.Context, subject SubjectLike, relation RelationLike, object ObjectLike, opts ...ExplainOption) (*Trace, error) {
	resolved := applyExplain(opts)

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

	// Cache lookup. c.cache is the shared Cache field; when it also
	// implements ExplainCache we consult / populate the Explain family.
	// maxNodes is part of the key because different caps produce
	// different traces (truncation flips). Miss on the type assertion
	// (Check-only Cache impl) → fall through to the DB.
	explainCache, cacheOK := c.cache.(ExplainCache)
	if cacheOK {
		if trace, cachedErr, found := explainCache.GetExplain(subj, rel, obj, resolved.maxNodes); found {
			return trace, cachedErr
		}
	}

	var maxNodes any
	if resolved.maxNodes > 0 {
		maxNodes = resolved.maxNodes
	}

	var raw []byte
	err := c.q.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5, $6)::text", prefixIdent("explain_permission", c.databaseSchema)),
		subj.Type, subj.ID, rel, obj.Type, obj.ID, maxNodes,
	).Scan(&raw)
	if err != nil {
		return nil, c.mapError("explain_permission", err)
	}

	var trace Trace
	if err := json.Unmarshal(raw, &trace); err != nil {
		return nil, fmt.Errorf("explain_permission: decoding trace: %w", err)
	}
	if cacheOK {
		explainCache.SetExplain(subj, rel, obj, resolved.maxNodes, &trace, nil)
	}
	return &trace, nil
}
