package sqlgen

import (
	"reflect"
	"slices"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// relationReferencesReviewedFields lists every RelationAnalysis field that has
// been reviewed for relation-reference edges. Fields carrying references MUST
// be walked by relationReferences: a missed edge lets listCompositionSafe
// approve a cyclic composition, and generated list functions have no runtime
// cycle guard — the result is infinite recursion at query time.
//
// When RelationAnalysis gains a field, this test fails. Decide whether the
// field carries (type, relation) references; if so, add it to
// relationReferences, then record it here either way.
var relationReferencesReviewedFields = map[string]bool{
	// Carries references — walked by relationReferences:
	"SatisfyingRelations":     true,
	"DirectImpliedBy":         true,
	"SimpleClosureRelations":  true,
	"ComplexClosureRelations": true,
	// A subset of ComplexClosureRelations, so its edges are already walked.
	"ImpliedCallees":               true,
	"IntersectionClosureRelations": true,
	"ExclusionClosureRelations":    true,
	"ExcludedRelations":            true,
	"SimpleExcludedRelations":      true,
	"ComplexExcludedRelations":     true,
	"ClosureExcludedRelations":     true,
	"ParentRelations":              true,
	"ClosureParentRelations":       true,
	"ExcludedParentRelations":      true,
	"UsersetPatterns":              true,
	"ClosureUsersetPatterns":       true,
	"IntersectionGroups":           true,
	"ExcludedIntersectionGroups":   true,
	"IndirectAnchor":               true,
	"SelfReferentialUsersets":      true,

	// Reviewed: no relation references.
	"ReachesItselfThroughParents": true, // a flag derived from ParentRelations
	"ObjectType":                  true,
	"Relation":                    true,
	"Features":                    true,
	"Capabilities":                true,
	"ListStrategy":                true,
	"HasComplexUsersetPatterns":   true,
	"DirectSubjectTypes":          true,
	"AllowedSubjectTypes":         true,
	"MaxUsersetDepth":             true,
	"ExceedsDepthLimit":           true,
	"HasSelfReferentialUserset":   true,
}

func TestRelationReferencesFieldCoverage(t *testing.T) {
	typ := reflect.TypeFor[RelationAnalysis]()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !relationReferencesReviewedFields[name] {
			t.Errorf("RelationAnalysis.%s has not been reviewed for relation-reference edges; "+
				"decide whether relationReferences must walk it, then add it to relationReferencesReviewedFields", name)
		}
	}
	for name := range relationReferencesReviewedFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("relationReferencesReviewedFields lists %s, which no longer exists on RelationAnalysis", name)
		}
	}
}

func TestRelationReferencesEdgeExtraction(t *testing.T) {
	a := &RelationAnalysis{
		ObjectType:                   "doc",
		Relation:                     "view",
		SatisfyingRelations:          []string{"view", "edit"},
		DirectImpliedBy:              []string{"edit"},
		SimpleClosureRelations:       []string{"simple"},
		ComplexClosureRelations:      []string{"complex"},
		IntersectionClosureRelations: []string{"inter"},
		ExclusionClosureRelations:    []string{"excluded_base"},
		ExcludedRelations:            []string{"banned"},
		SimpleExcludedRelations:      []string{"sbanned"},
		ComplexExcludedRelations:     []string{"cbanned"},
		ClosureExcludedRelations:     []string{"clbanned"},
		ParentRelations: []ParentRelationInfo{{
			Relation: "view", AllowedLinkingTypes: []string{"folder"},
		}},
		ClosureParentRelations: []ParentRelationInfo{{
			Relation: "admin", AllowedLinkingTypes: []string{"org"},
		}},
		ExcludedParentRelations: []ParentRelationInfo{{
			Relation: "blocked", AllowedLinkingTypes: []string{"org"},
		}},
		UsersetPatterns: []UsersetPattern{{
			SubjectType: "group", SubjectRelation: "member",
		}},
		ClosureUsersetPatterns: []UsersetPattern{{
			SubjectType: "team", SubjectRelation: "member",
		}},
		IntersectionGroups: []IntersectionGroupInfo{{
			Parts: []IntersectionPart{{
				Relation:         "writer",
				ExcludedRelation: "frozen",
				ParentRelation:   &ParentRelationInfo{Relation: "owner", AllowedLinkingTypes: []string{"repo"}},
			}},
		}},
		IndirectAnchor: &IndirectAnchorInfo{
			AnchorType:     "space",
			AnchorRelation: "viewer",
			Path: []AnchorPathStep{{
				Type: "userset", SubjectType: "guild", SubjectRelation: "member",
			}},
		},
	}

	got := make(map[string]bool)
	for _, ref := range relationReferences(a) {
		got[ref] = true
	}

	want := []string{
		"doc.view", "doc.edit", "doc.simple", "doc.complex", "doc.inter",
		"doc.excluded_base",
		"doc.banned", "doc.sbanned", "doc.cbanned", "doc.clbanned",
		"folder.view", "org.admin", "org.blocked",
		"group.member", "team.member",
		"doc.writer", "doc.frozen", "repo.owner",
		"space.viewer", "guild.member",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("relationReferences missing edge %q", w)
		}
	}
}

// A tuple-to-userset inside an excluded intersection reads the parent relation
// on every type the link allows. Its linking types used to be left empty, so
// relationReferences named the parent relation on no type at all: the folder
// edges below were missing, which the exposure closure and the list
// composition graph both build on.
func TestRelationReferences_ExcludedIntersectionParents(t *testing.T) {
	types, err := parser.ParseSchemaString(`model
  schema 1.1
type user
type folder
  relations
    define a: [user]
    define b: [user]
type document
  relations
    define v: [user]
    define owner: [folder]
    define r: v but not (a from owner and b from owner)
`)
	if err != nil {
		t.Fatal(err)
	}
	analyses := ComputeCanGenerate(AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
	i := slices.IndexFunc(analyses, func(a RelationAnalysis) bool { return a.ObjectType == "document" && a.Relation == "r" })
	if i < 0 {
		t.Fatal("document#r not analyzed")
	}
	for _, p := range analyses[i].ExcludedIntersectionGroups[0].Parts {
		if got := p.ParentRelation.AllowedLinkingTypes; !slices.Equal(got, []string{"folder"}) {
			t.Errorf("excluded part %s from owner: AllowedLinkingTypes = %v, want [folder]", p.Relation, got)
		}
	}
	refs := relationReferences(&analyses[i])
	for _, want := range []string{"folder.a", "folder.b"} {
		if !slices.Contains(refs, want) {
			t.Errorf("relationReferences(document#r) = %v, missing %s", refs, want)
		}
	}
}
