package melange

import (
	"fmt"
	"strings"
)

// ListObjectsOption configures a single Checker.ListObjects call.
//
// Like the Expand options, object filtering is a Melange extension: OpenFGA's
// ListObjects takes no object-side filter, and the equivalent request
// (openfga/openfga#301) was declined upstream because filtering a general
// relation would need a second graph expansion per query. Melange can afford
// the narrow version because the filter becomes a semi-join the query planner
// resolves alongside the main scan.
type ListObjectsOption func(*listObjectsOpts)

// listObjectsOpts holds resolved ListObjects options. The zero value means
// "no extensions in effect" — full OpenFGA-equivalent behavior.
type listObjectsOpts struct {
	filter string // "" => no filter
	err    error  // set by an option that was handed invalid input
}

// applyListObjects runs each option against a fresh listObjectsOpts and
// validates the result.
//
// Validation lives here rather than at each call site so a new ListObjects
// entry point cannot forget it. It diverges from applyExpand/applyExplain,
// which return no error, because those options cannot express an invalid state
// and an object filter can.
func applyListObjects(options []ListObjectsOption) (listObjectsOpts, error) {
	var o listObjectsOpts
	for _, opt := range options {
		opt(&o)
	}
	return o, o.err
}

// param returns the value to bind to the generated p_filter argument: the
// filter string, or nil so SQL sees NULL and skips filtering entirely.
func (o listObjectsOpts) param() any {
	if o.filter == "" {
		return nil
	}
	return o.filter
}

// WithObjectFilter narrows results to objects that hold relation to subject,
// evaluated against stored tuples rather than the permission graph.
//
// For "elements this user can view, but only inside workspace:7":
//
//	checker.ListObjects(ctx, authz.User("1"), authz.RelView, authz.TypeElement, page,
//	    melange.WithObjectFilter(authz.RelWorkspace, authz.Workspace("7")))
//
// The filter must name a directly-assignable relation on the object type being
// listed. The generated function knows that set and rejects anything else —
// including a computed or tuple-to-userset relation, or a typo — rather than
// matching no tuples and returning an empty list that reads like "no access".
//
// Filtering is applied before pagination, so limits and cursors count filtered
// rows. Relations whose list strategy is recursive or composed still return the
// correct filtered set, but resolve it by filtering after enumeration rather
// than before, so they see correctness without the speedup.
func WithObjectFilter(relation RelationLike, subject ObjectLike) ListObjectsOption {
	return func(o *listObjectsOpts) {
		rel := string(relation.FGARelation())
		obj := subject.FGAObject()
		if err := validateObjectFilterParts(rel, obj); err != nil {
			o.err = err
			return
		}
		o.filter = fmt.Sprintf("%s@%s:%s", rel, obj.Type, obj.ID)
	}
}

// validateObjectFilterParts rejects filter components that would not survive
// the round trip through the single wire parameter.
//
// This has to run on the parts, not on the encoded string, because encoding is
// lossy in exactly the case that matters. The generated function splits the
// subject at its FIRST colon so that ids may contain colons — which means
// `tenant@tenant:eu:west:1` (type "tenant", id "eu:west:1") and a filter whose
// type was "tenant:eu" are the same string. Once encoded, nothing can tell them
// apart; here the caller's intent is still visible.
func validateObjectFilterParts(relation string, subject Object) error {
	if relation == "" {
		return fmt.Errorf("%w: relation is required", ErrInvalidObjectFilter)
	}
	if subject.Type == "" || subject.ID == "" {
		return fmt.Errorf("%w: subject needs both a type and an id, got %q:%q",
			ErrInvalidObjectFilter, subject.Type, subject.ID)
	}
	// '@' and ':' are how the wire form is split, '#' marks a userset.
	if strings.ContainsAny(relation, "@:#") {
		return fmt.Errorf("%w: relation %q cannot contain '@', ':' or '#'",
			ErrInvalidObjectFilter, relation)
	}
	if strings.ContainsAny(string(subject.Type), "@:#") {
		return fmt.Errorf("%w: subject type %q cannot contain '@', ':' or '#'",
			ErrInvalidObjectFilter, subject.Type)
	}
	// Only direct relations are filterable, so a userset subject cannot match.
	if strings.Contains(subject.ID, "#") {
		return fmt.Errorf("%w: subject id %q names a userset; only direct relations are filterable",
			ErrInvalidObjectFilter, subject.ID)
	}
	return nil
}
