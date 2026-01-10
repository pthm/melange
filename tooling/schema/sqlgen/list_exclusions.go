package sqlgen

// ExcludedParentRelation represents a TTU exclusion pattern like "but not viewer from parent".
type ExcludedParentRelation struct {
	Relation            string
	LinkingRelation     string
	AllowedLinkingTypes []string
}

// ExcludedIntersectionPart represents a single part of an intersection exclusion.
type ExcludedIntersectionPart struct {
	Relation         string
	ExcludedRelation string
	ParentRelation   *ExcludedParentRelation
}

// ExcludedIntersectionGroup represents an intersection exclusion like "but not (editor and owner)".
type ExcludedIntersectionGroup struct {
	Parts []ExcludedIntersectionPart
}

// ExclusionInput contains the data needed to generate exclusion predicates.
type ExclusionInput struct {
	ObjectType string

	ObjectIDExpr    string
	SubjectTypeExpr string
	SubjectIDExpr   string

	SimpleExcludedRelations  []string
	ComplexExcludedRelations []string
	ExcludedParentRelations  []ExcludedParentRelation
	ExcludedIntersection     []ExcludedIntersectionGroup
}
