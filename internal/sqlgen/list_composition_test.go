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
// A missed edge also leaves a function out of a restricted Exposure's
// installed set, so the call that needs it denies silently.
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
			Parts: []IntersectionPart{
				{Relation: "writer", ExcludedRelation: "frozen"},
				{Relation: "owner", ParentRelation: &ParentRelationInfo{Relation: "owner", AllowedLinkingTypes: []string{"repo"}}},
			},
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
	// The TTU part reads owner on repo only, never on doc.
	if got["doc.owner"] {
		t.Errorf("relationReferences has edge %q from a tuple-to-userset intersection part", "doc.owner")
	}

	// The analyzer never sets ExcludedRelation on a TTU part (see
	// TestIntersectionTTUPartsCarryNoExclusion), but check and list_objects
	// codegen would apply it on the object's own type, so the edge stays.
	ttu := &RelationAnalysis{
		ObjectType: "doc",
		Relation:   "view",
		IntersectionGroups: []IntersectionGroupInfo{{
			Parts: []IntersectionPart{{
				Relation:         "owner",
				ExcludedRelation: "frozen2",
				ParentRelation:   &ParentRelationInfo{Relation: "owner", AllowedLinkingTypes: []string{"repo"}},
			}},
		}},
	}
	refs := relationReferences(ttu)
	for _, w := range []string{"doc.frozen2", "repo.owner"} {
		if !slices.Contains(refs, w) {
			t.Errorf("relationReferences(TTU part with an exclusion) = %v, missing %s", refs, w)
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

// intersectionThisStandaloneModel is the model of the
// intersection_this_standalone YAML case: document#and_ttu's TTU part reads
// a on folder, and document defines an a of its own.
const intersectionThisStandaloneModel = `model
  schema 1.1
type user
type folder
  relations
    define a: [user]
type document
  relations
    define parent: [folder]
    define a: [user]
    define c: [user]
    define and_ttu: [user] and a from parent
    define and_or_implied: ([user] and a) or c
    define and_or_ttu: ([user] and a) or a from parent
`

// mustAnalysis returns the analysis of typ#rel in m.
func mustAnalysis(t *testing.T, m exposureModel, typ, rel string) *RelationAnalysis {
	t.Helper()
	i := slices.IndexFunc(m.analyses, func(a RelationAnalysis) bool { return a.ObjectType == typ && a.Relation == rel })
	if i < 0 {
		t.Fatalf("%s#%s not analyzed", typ, rel)
	}
	return &m.analyses[i]
}

// A tuple-to-userset intersection part reads its relation on the parent's
// types only, inside an intersection and inside an excluded one, even when
// the object's own type defines a relation of the same name.
func TestRelationReferences_IntersectionTTUPartOnParentTypeOnly(t *testing.T) {
	for _, tc := range []struct {
		name, model, rel string
		want, notWant    []string
	}{
		{
			name: "intersection", model: intersectionThisStandaloneModel, rel: "and_ttu",
			want: []string{"folder.a"}, notWant: []string{"document.a"},
		},
		{
			name: "excluded intersection", rel: "r",
			model: `model
  schema 1.1
type user
type folder
  relations
    define a: [user]
type document
  relations
    define owner: [folder]
    define a: [user]
    define b: [user]
    define v: [user]
    define r: v but not (a from owner and b)
`,
			want: []string{"document.v", "folder.a", "document.b"}, notWant: []string{"document.a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := relationReferences(mustAnalysis(t, mustExposureModel(t, tc.model), "document", tc.rel))
			for _, w := range tc.want {
				if !slices.Contains(refs, w) {
					t.Errorf("relationReferences(document#%s) = %v, missing %s", tc.rel, refs, w)
				}
			}
			for _, w := range tc.notWant {
				if slices.Contains(refs, w) {
					t.Errorf("relationReferences(document#%s) = %v, has %s", tc.rel, refs, w)
				}
			}
		})
	}
}

// relationReferences gives a tuple-to-userset intersection part no edge for
// its relation on the object's own type, and keeps its ExcludedRelation
// edge only defensively. Both rest on the analyzer building every TTU part
// with Relation equal to the parent relation and no exclusion: the parser
// attaches exclusions to relation and This members only. This fails if a
// corpus model's analysis breaks that.
func TestIntersectionTTUPartsCarryNoExclusion(t *testing.T) {
	for _, m := range exposureCorpus(t) {
		for _, a := range m.analyses {
			for _, g := range slices.Concat(a.IntersectionGroups, a.ExcludedIntersectionGroups) {
				for _, p := range g.Parts {
					if p.ParentRelation == nil {
						continue
					}
					if p.ExcludedRelation != "" || p.Relation != p.ParentRelation.Relation {
						t.Errorf("%s: %s#%s has TTU part %+v (parent %+v), want no exclusion and Relation = the parent relation",
							m.name, a.ObjectType, a.Relation, p, *p.ParentRelation)
					}
				}
			}
		}
	}
}

// document#v's TTU part reads a on folder, not document#a, so composing
// list_document_a_obj against list_document_v_obj is acyclic, and the
// exclusion anti-joins it instead of checking v per row. The edge
// relationReferences used to add for the part made it look like a cycle.
// The genuine edge from document#a to document#v still refuses the reverse
// composition.
func TestListCompositionSafe_TTUPartNoFalseCycle(t *testing.T) {
	m := mustExposureModel(t, `model
  schema 1.1
type user
type folder
  relations
    define a: [user]
type document
  relations
    define parent: [folder]
    define v: [user] and a from parent
    define a: [user] but not v
`)
	g := newRelationGraph(buildAnalysisLookup(m.analyses))
	if !g.listCompositionSafe("document", "a", "document", "v") {
		t.Error("composing document#a against document#v must be safe: v does not read document#a")
	}
	if g.reaches(g.ids["document.v"], g.ids["document.a"]) {
		t.Error("document#v must not reach document#a")
	}
	if g.listCompositionSafe("document", "v", "document", "a") {
		t.Error("composing document#v against document#a must be unsafe: a reads v")
	}
	sql := functionSQL(generateExposed(t, m, Exposure{}).named)["list_document_a_obj"]
	expectSQL(t, "list_document_a_obj", sql, `FROM "authz"."list_document_v_obj"(`)
}
