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
}

// applyListObjects runs each option against a fresh listObjectsOpts.
func applyListObjects(options []ListObjectsOption) listObjectsOpts {
	var o listObjectsOpts
	for _, opt := range options {
		opt(&o)
	}
	return o
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
// listed. A computed or tuple-to-userset relation is rejected by the generated
// function rather than silently mis-scoping the result.
//
// Filtering is applied before pagination, so limits and cursors count filtered
// rows. Relations whose list strategy is recursive or composed still return the
// correct filtered set, but resolve it by filtering after enumeration rather
// than before, so they see correctness without the speedup.
func WithObjectFilter(relation RelationLike, subject ObjectLike) ListObjectsOption {
	return func(o *listObjectsOpts) {
		obj := subject.FGAObject()
		o.filter = fmt.Sprintf("%s@%s:%s", relation.FGARelation(), obj.Type, obj.ID)
	}
}

// validateObjectFilter rejects a filter whose parts contain the delimiters the
// generated function parses on, which would otherwise reach SQL as a filter
// naming a different relation or subject than the caller asked for.
func validateObjectFilter(filter string) error {
	if filter == "" {
		return nil
	}
	rel, subject, ok := strings.Cut(filter, "@")
	if !ok || rel == "" {
		return fmt.Errorf("%w: object filter %q has no relation before '@'", ErrInvalidObjectFilter, filter)
	}
	subjType, subjID, ok := strings.Cut(subject, ":")
	if !ok || subjType == "" || subjID == "" {
		return fmt.Errorf("%w: object filter %q needs a subject of the form type:id", ErrInvalidObjectFilter, filter)
	}
	if strings.ContainsAny(rel, "@:#") || strings.ContainsAny(subjType, "@#") {
		return fmt.Errorf("%w: object filter %q contains a delimiter in its relation or subject type", ErrInvalidObjectFilter, filter)
	}
	if strings.Contains(subjID, "#") {
		return fmt.Errorf("%w: object filter %q names a userset subject; only direct relations are filterable", ErrInvalidObjectFilter, filter)
	}
	return nil
}
