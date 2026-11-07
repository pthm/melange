package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// accessible_{type}_{relation}_by_{link}(p_subject_type, p_subject_id) returns
// the IDs of the parent objects through which the subject holds relation on
// objects of type, for a relation whose every grant is a parent relation over
// one link to one parent type (subject.can_read: subject_reader from org).
// An application with the link in a column filters on that column:
//
//	WHERE s.organization_id IN (SELECT * FROM accessible_subject_can_read_by_org('user', $1))
//
// which reads the parents' few IDs instead of the access set of every object
// under them: on phoebe's production-shaped data, counting a 1M-subject org
// took 4.6s through accessible_subject_can_read and 70ms through this form.
//
// It is exact when each object has one link, held in that column, and does not
// cover a subject that is a userset on the object type itself
// (subject:s#can_read holds can_read on subject s only).

// parentKeyRelation is a relation whose grants are all parent relations
// through one link to one parent type.
type parentKeyRelation struct {
	ObjectType, Relation string
	Link, ParentType     string
	ParentRelations      []string // the relations on the parent that grant it, in model order
}

// functionName returns the name of pk's parent-key accessible function.
func (pk parentKeyRelation) functionName() string {
	// The link is part of the shortened name, not a suffix, so a long one is
	// truncated and hashed instead of eating the accessible_ prefix the
	// migrator recognizes generated functions by.
	return SafeIdentifier("accessible_", pk.ObjectType, pk.Relation+"_by_"+pk.Link, "")
}

// parentKeyRelations returns the relations that get a parent-key function. A
// name that another accessible function already has is skipped: the
// relation's own accessible function still covers it.
func parentKeyRelations(analyses []RelationAnalysis) []parentKeyRelation {
	lookup := buildAnalysisLookup(analyses)
	taken := make(map[string]bool, len(analyses))
	for _, a := range analyses {
		if a.Capabilities.ListAllowed {
			taken[AccessibleObjectsFunctionName(a.ObjectType, a.Relation)] = true
		}
	}
	var out []parentKeyRelation
	for _, a := range analyses {
		pk, ok := parentKeyRelationOf(a, lookup)
		if !ok {
			continue
		}
		name := pk.functionName()
		if taken[name] {
			continue
		}
		taken[name] = true
		out = append(out, pk)
	}
	return out
}

// ParentKeyFunction is a generated parent-key accessible function: Name
// returns the IDs of the ParentType objects through whose Link the subject
// holds Relation on ObjectType objects.
type ParentKeyFunction struct {
	ObjectType, Relation, Link, ParentType, Name string
}

// ParentKeyFunctions returns the parent-key accessible functions generated
// for analyses.
func ParentKeyFunctions(analyses []RelationAnalysis) []ParentKeyFunction {
	pks := parentKeyRelations(analyses)
	out := make([]ParentKeyFunction, len(pks))
	for i, pk := range pks {
		out[i] = ParentKeyFunction{ObjectType: pk.ObjectType, Relation: pk.Relation, Link: pk.Link, ParentType: pk.ParentType, Name: pk.functionName()}
	}
	return out
}

// parentKeyRelationOf reports whether a's grants are all parent relations
// through one link to one parent type, each listable on the parent. Every
// relation in a's closure must grant through parent relations only: a direct,
// userset or wildcard grant reaches objects without the link, and an
// exclusion or intersection removes objects the parent set would keep. The
// parent relations are collected across the closure, since a relation lists
// only its own (can_read: reader or admin from org, reader: member from org).
func parentKeyRelationOf(a RelationAnalysis, lookup map[string]*RelationAnalysis) (parentKeyRelation, bool) {
	if !a.Capabilities.ListAllowed {
		return parentKeyRelation{}, false
	}
	var parents []ParentRelationInfo
	for _, rel := range a.SatisfyingRelations {
		s := lookup[a.ObjectType+"."+rel]
		if s == nil || !grantsThroughParentsOnly(*s) {
			return parentKeyRelation{}, false
		}
		parents = append(parents, s.ParentRelations...)
	}
	if len(parents) == 0 {
		return parentKeyRelation{}, false
	}
	pk := parentKeyRelation{ObjectType: a.ObjectType, Relation: a.Relation}
	for _, p := range parents {
		if len(p.AllowedLinkingTypes) != 1 {
			return parentKeyRelation{}, false
		}
		if pk.Link == "" {
			pk.Link, pk.ParentType = p.LinkingRelation, p.AllowedLinkingTypes[0]
		}
		if p.LinkingRelation != pk.Link || p.AllowedLinkingTypes[0] != pk.ParentType {
			return parentKeyRelation{}, false
		}
		if target := lookup[pk.ParentType+"."+p.Relation]; target == nil || !target.Capabilities.ListAllowed {
			return parentKeyRelation{}, false
		}
		if !slices.Contains(pk.ParentRelations, p.Relation) {
			pk.ParentRelations = append(pk.ParentRelations, p.Relation)
		}
	}
	return pk, true
}

// grantsThroughParentsOnly reports whether a relation's own grants are parent
// relations or other relations, with nothing excluded or intersected.
func grantsThroughParentsOnly(a RelationAnalysis) bool {
	f := a.Features
	return !f.HasDirect && !f.HasWildcard && !f.HasUserset && !f.HasExclusion && !f.HasIntersection &&
		len(a.ExcludedParentRelations) == 0 && len(a.ExcludedIntersectionGroups) == 0
}

// parentKeyRows is the planner's row estimate for a parent-key function: a
// subject reaches a few parents. With the default of 1000, a query filtering
// by the function hashes the whole child table instead of reading the
// parents' rows through an index.
const parentKeyRows = 10

// renderParentKeyFunction renders the parent-key accessible function of pk:
// the distinct union of the parent relations' accessible sets.
//
// Unlike the accessible functions it is PL/pgSQL, so it is not inlined into
// the caller's query: inlined, the union of the parents' accessible sets is
// planned with every call of the application's query, which is most of the
// time for a subject with few parents, and its row estimate comes from the
// union. Here it is planned once per session, and the planner sees
// parentKeyRows. The IDs are distinct, so joining on them repeats no rows.
func renderParentKeyFunction(schema string, pk parentKeyRelation) string {
	// UNION makes the IDs distinct; a single arm needs DISTINCT instead.
	distinct := ""
	if len(pk.ParentRelations) == 1 {
		distinct = "DISTINCT "
	}
	arms := make([]string, len(pk.ParentRelations))
	for i, rel := range pk.ParentRelations {
		arms[i] = fmt.Sprintf("SELECT %sk.object_id FROM %s(p_subject_type, p_subject_id, %s) AS k",
			distinct, sqldsl.PrefixIdent(accessibleSubjectKindFunctionName(pk.ParentType, rel), schema), subjectIsUserset)
	}
	name := pk.functionName()
	fn := PlpgsqlFunction{
		Schema: schema,
		Name:   name,
		Args: []FuncArg{
			{Name: "p_subject_type", Type: "TEXT"},
			{Name: "p_subject_id", Type: "TEXT"},
		},
		Returns: fmt.Sprintf("TABLE(object_id TEXT) ROWS %d", parentKeyRows),
		Header: []string{
			fmt.Sprintf("Objects of type %s through whose %s link the subject holds %s on %s objects.", pk.ParentType, pk.Link, pk.Relation, pk.ObjectType),
			fmt.Sprintf("Filter %s rows by the column holding their %s:", pk.ObjectType, pk.Link),
			fmt.Sprintf("WHERE <%s column> IN (SELECT * FROM %s(subject_type, subject_id)).", pk.Link, sqldsl.PrefixIdent(name, schema)),
			fmt.Sprintf("Exact when each %s has one %s; a subject that is a userset on %s itself", pk.ObjectType, pk.Link, pk.ObjectType),
			"is not covered.",
		},
		Body: []Stmt{ReturnQuery{Query: strings.Join(arms, "\n    UNION\n    ")}},
	}
	return fn.SQL()
}
