package sqlgen

import (
	"cmp"
	"slices"
)

// AccessibleFunction is an accessible function a migration installs for an
// exposed relation, under the unqualified name it is created with. Names
// longer than PostgreSQL's 63 bytes are truncated and hashed, so callers use
// Name instead of building it from the type and relation.
//
// With Link empty it is accessible_<t>_<r>(subject_type, subject_id), the IDs
// of the ObjectType objects on which the subject holds Relation. With Link
// set it is the parent-key form accessible_<t>_<r>_by_<link>(subject_type,
// subject_id), the IDs of the ParentType objects through whose Link the
// subject holds Relation on ObjectType objects.
type AccessibleFunction struct {
	ObjectType, Relation string
	Link, ParentType     string
	Name                 string
}

// AccessibleFunctions returns the accessible functions a migration under
// exposure installs for the relations it exposes: accessible_<t>_<r> for
// each listable relation the accessible family exposes, and the parent-key
// form for each exposed relation that has one when parent_key is on. It
// leaves out accessible_objects and the accessible functions installed only
// because an exposed relation resolves through them. analyses must be the
// whole model, since parent-key names are de-duplicated across it. The result
// is sorted by ObjectType, Relation and Link.
func AccessibleFunctions(analyses []RelationAnalysis, exposure Exposure) ([]AccessibleFunction, error) {
	if err := exposure.Validate(); err != nil {
		return nil, err
	}
	c := newExposureClosure(analyses, exposure)
	var out []AccessibleFunction
	for _, f := range installedAccessibleFunctions(analyses, c) {
		// Parent-key functions are installed only for exposed relations.
		if f.Link != "" || c.public(familyAccessible, f.ObjectType, f.Relation) {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b AccessibleFunction) int {
		return cmp.Or(cmp.Compare(a.ObjectType, b.ObjectType), cmp.Compare(a.Relation, b.Relation), cmp.Compare(a.Link, b.Link))
	})
	return out, nil
}

// installedAccessibleFunctions returns every accessible function generation
// emits under the exposure, per-relation forms in analysis order, then the
// parent-key forms. It follows the gates of GenerateListSQLWithOptions and
// CollectFunctionNamesWithOptions; TestAccessibleFunctions_MatchGenerated
// checks the three agree.
func installedAccessibleFunctions(analyses []RelationAnalysis, c *exposureClosure) []AccessibleFunction {
	var out []AccessibleFunction
	for _, a := range analyses {
		if a.Capabilities.ListAllowed && c.installsListObjects(a.ObjectType, a.Relation) {
			out = append(out, AccessibleFunction{ObjectType: a.ObjectType, Relation: a.Relation, Name: AccessibleObjectsFunctionName(a.ObjectType, a.Relation)})
		}
	}
	for _, pk := range parentKeyRelations(analyses, c) {
		out = append(out, AccessibleFunction{ObjectType: pk.ObjectType, Relation: pk.Relation, Link: pk.Link, ParentType: pk.ParentType, Name: pk.functionName()})
	}
	return out
}
