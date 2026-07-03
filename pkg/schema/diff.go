package schema

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeClass classifies a schema change by its access impact.
type ChangeClass string

const (
	// ClassAdditive widens access or is otherwise safe to apply: a new type or
	// relation, a new grant, or a removed restriction. Nobody loses access.
	ClassAdditive ChangeClass = "additive"
	// ClassBreaking narrows access or removes structure: a removed type or
	// relation, a removed grant, or a new restriction. Someone may lose access.
	ClassBreaking ChangeClass = "breaking"
)

// Change is a single difference between two schemas.
type Change struct {
	Class    ChangeClass `json:"class"`
	Type     string      `json:"type"`               // object type name
	Relation string      `json:"relation,omitempty"` // empty for type-level changes
	Summary  string      `json:"summary"`            // human-readable description
}

// SchemaDiff is the set of changes turning an "old" schema into a "new" one.
type SchemaDiff struct {
	Changes []Change `json:"changes"`
}

// Empty reports whether the two schemas are equivalent.
func (d SchemaDiff) Empty() bool { return len(d.Changes) == 0 }

// HasBreaking reports whether any change narrows access.
func (d SchemaDiff) HasBreaking() bool {
	for _, c := range d.Changes {
		if c.Class == ClassBreaking {
			return true
		}
	}
	return false
}

// BreakingSummaries returns the human-readable summaries of the breaking changes.
func (d SchemaDiff) BreakingSummaries() []string {
	var out []string
	for _, c := range d.Changes {
		if c.Class == ClassBreaking {
			out = append(out, c.Summary)
		}
	}
	return out
}

// Counts returns how many additive and breaking changes there are.
func (d SchemaDiff) Counts() (additive, breaking int) {
	for _, c := range d.Changes {
		if c.Class == ClassBreaking {
			breaking++
		} else {
			additive++
		}
	}
	return additive, breaking
}

// Diff compares two parsed models and returns the changes turning old into new,
// each classified additive (safe) or breaking (narrows access). Comparison is
// order-independent: relations are compared as sets of grant and exclusion
// terms, so re-ordering subject types or implied relations is not a change.
//
// The comparison is over melange's PARSED model — the same structures it
// compiles to SQL — so the diff reflects actual runtime behavior, including any
// flattening the parser applies to complex nesting (e.g. `(this or X) and Y`).
//
// Classification accounts for implied-by closure, intersection subsumption, and
// exclusion polarity (widening a relation used in — or implying one used in — a
// `but not` narrows the excluder), but is a conservative heuristic. Its members
// are compared structurally in a few spots: intersection member tokens are not
// resolved through implication, wildcard (`[t:*]`) and specific (`[t]`) grants
// are distinct capabilities, and exclusions nested inside intersection members
// are not subsumed. So a change of those rare shapes may over-report — an extra
// additive or breaking line — but it never UNDER-reports breaking relative to
// melange's compiled model, which is what makes it safe as a CI gate.
func Diff(oldModel, newModel []TypeDefinition) SchemaDiff {
	oldTypes := indexTypes(oldModel)
	newTypes := indexTypes(newModel)

	var changes []Change

	for name, nt := range newTypes {
		ot, existed := oldTypes[name]
		if !existed {
			changes = append(changes, Change{ClassAdditive, name, "", fmt.Sprintf("type %s added", name)})
			continue
		}
		changes = append(changes, diffRelations(name, ot, nt)...)
	}
	for name := range oldTypes {
		if _, still := newTypes[name]; !still {
			changes = append(changes, Change{ClassBreaking, name, "", fmt.Sprintf("type %s removed", name)})
		}
	}

	sortChanges(changes)
	return SchemaDiff{Changes: changes}
}

func diffRelations(typeName string, oldT, newT TypeDefinition) []Change {
	oldRels := indexRelations(oldT)
	newRels := indexRelations(newT)

	// Transitive implied-by closure per side, so a grant that is removed but
	// still reachable through another relation (e.g. dropping `or owner` from
	// `viewer` when `editor` is already implied by `owner`) is not a change.
	oldImpliers := typeImpliers(oldT)
	newImpliers := typeImpliers(newT)

	// Relations whose widening narrows an excluder: those used as an exclusion
	// (`but not X`), plus every relation that transitively implies one — widening
	// an implier widens the excluded relation too. Grant additions to any of
	// these are breaking.
	exclRefs := map[string]bool{}
	for name := range exclusionRefs(oldT, newT) {
		exclRefs[name] = true
		for _, impl := range oldImpliers[name] {
			exclRefs[impl] = true
		}
		for _, impl := range newImpliers[name] {
			exclRefs[impl] = true
		}
	}

	var changes []Change
	for name, nr := range newRels {
		or, existed := oldRels[name]
		if !existed {
			changes = append(changes, Change{
				ClassAdditive, typeName, name,
				fmt.Sprintf("relation %s.%s added", typeName, name),
			})
			continue
		}
		changes = append(changes, diffRelation(typeName, or, nr, oldImpliers, newImpliers, exclRefs)...)
	}
	for name := range oldRels {
		if _, still := newRels[name]; !still {
			changes = append(changes, Change{
				ClassBreaking, typeName, name,
				fmt.Sprintf("relation %s.%s removed", typeName, name),
			})
		}
	}
	return changes
}

// typeImpliers returns, for each relation in the type, the set of relations that
// transitively imply it (its computed-userset closure).
func typeImpliers(t TypeDefinition) map[string][]string {
	graph := make(map[string][]string, len(t.Relations))
	for _, r := range t.Relations {
		graph[r.Name] = append(graph[r.Name], r.ImpliedBy...)
	}
	return computeTransitiveClosure(graph)
}

// exclusionRefs returns the set of relation names referenced by any exclusion
// (`but not X`) across the given type definitions.
func exclusionRefs(types ...TypeDefinition) map[string]bool {
	refs := map[string]bool{}
	for _, t := range types {
		for _, r := range t.Relations {
			for _, ex := range r.ExcludedRelations {
				refs[ex] = true
			}
			for _, ep := range r.ExcludedParentRelations {
				refs[ep.Relation] = true
			}
			for _, g := range r.ExcludedIntersectionGroups {
				for _, rel := range g.Relations {
					refs[rel] = true
				}
			}
			for _, g := range r.IntersectionGroups {
				for _, excls := range g.Exclusions {
					for _, ex := range excls {
						refs[ex] = true
					}
				}
			}
		}
	}
	return refs
}

// andSet is a conjunction of requirement tokens — an AND of atoms. A relation
// grants access when ANY of its grant sets is fully satisfied (an OR of ANDs, i.e.
// disjunctive normal form), and excludes when ANY of its exclusion sets holds.
type andSet map[string]bool

// diffRelation compares one relation via its grant and exclusion sets, using
// implication (subset covering) rather than exact equality — so loosening an
// intersection (`a and b` → `a`) widens access and reads as additive, while
// tightening it reads as breaking.
func diffRelation(typeName string, oldR, newR RelationDefinition, oldImpliers, newImpliers map[string][]string, exclRefs map[string]bool) []Change {
	label := typeName + "." + newR.Name
	var changes []Change

	oldG, newG := grantSets(oldR, oldImpliers), grantSets(newR, newImpliers)
	// A grant path is lost when no surviving grant set implies it — that narrows
	// access. `[t]` (specific) and `[t:*]` (public) are distinct grant
	// capabilities: adding either widens (additive), removing either narrows
	// (breaking). Swapping `[t]` for `[t:*]` therefore reports both, which is the
	// conservative, fail-safe result since the specific-grant capability is lost.
	for _, g := range uncovered(oldG, newG) {
		changes = append(changes, Change{ClassBreaking, typeName, newR.Name, label + " no longer grants " + renderAndSet(g)})
	}
	// Adding a grant widens access — additive — unless this relation is used as
	// an exclusion elsewhere, in which case widening it narrows the excluder, so
	// it is (conservatively) breaking.
	for _, g := range uncovered(newG, oldG) {
		if exclRefs[newR.Name] {
			changes = append(changes, Change{
				ClassBreaking, typeName, newR.Name,
				label + " grants " + renderAndSet(g) + " (used in an exclusion — widens who is excluded)",
			})
		} else {
			changes = append(changes, Change{ClassAdditive, typeName, newR.Name, label + " grants " + renderAndSet(g)})
		}
	}

	// Exclusions are the reverse: a new exclusion not implied by an old one
	// narrows access (breaking); an old one no longer implied widens it.
	oldX, newX := exclusionSets(oldR), exclusionSets(newR)
	for _, x := range uncovered(newX, oldX) {
		changes = append(changes, Change{ClassBreaking, typeName, newR.Name, label + " excludes " + renderAndSet(x)})
	}
	for _, x := range uncovered(oldX, newX) {
		changes = append(changes, Change{ClassAdditive, typeName, newR.Name, label + " no longer excludes " + renderAndSet(x)})
	}
	return changes
}

// uncovered returns the sets in `sets` that no set in `others` covers. A set o
// covers s when o ⊆ s: o requires no more than s, so satisfying s satisfies o —
// o grants at least everything s does, making s redundant given o.
func uncovered(sets, others []andSet) []andSet {
	var out []andSet
	for _, s := range sets {
		isCovered := false
		for _, o := range others {
			if subsetOf(o, s) {
				isCovered = true
				break
			}
		}
		if !isCovered {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return renderAndSet(out[i]) < renderAndSet(out[j]) })
	return out
}

// subsetOf reports whether every token of a is in b (a ⊆ b) — a requires no more
// than b, so satisfying b satisfies a.
func subsetOf(a, b andSet) bool {
	if len(a) > len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// grantSets returns the relation's grant paths, one andSet per OR alternative.
// Simple grants (implied relations via the closure, TTU) are singleton sets;
// each intersection group is the set of its ANDed members.
//
// The parser encodes `this and X` (e.g. `viewer: [user] and editor`) as an
// intersection group that references the relation's own name, with the direct
// subject types in SubjectTypeRefs. Those direct grants are therefore standalone
// ONLY when no intersection group self-references; otherwise they belong to the
// intersection (handled in intersectionGrantSets), so treating them as standalone
// would miss the tightening — a breaking change — entirely.
func grantSets(r RelationDefinition, impliers map[string][]string) []andSet {
	var sets []andSet
	if !hasSelfIntersection(r) {
		for _, ref := range r.SubjectTypeRefs {
			sets = append(sets, andSet{subjectTypeToken(ref): true})
		}
	}
	for _, impl := range impliers[r.Name] {
		sets = append(sets, andSet{impl: true})
	}
	for _, p := range r.ParentRelations {
		sets = append(sets, andSet{ttuToken(p): true})
	}
	for _, g := range r.IntersectionGroups {
		sets = append(sets, intersectionGrantSets(g, r)...)
	}
	return sets
}

// hasSelfIntersection reports whether any of the relation's intersection groups
// references the relation itself (the `this and X` shape).
func hasSelfIntersection(r RelationDefinition) bool {
	for _, g := range r.IntersectionGroups {
		for _, rel := range g.Relations {
			if rel == r.Name {
				return true
			}
		}
	}
	return false
}

// exclusionSets returns the relation's exclusion conditions, one andSet each.
func exclusionSets(r RelationDefinition) []andSet {
	sets := make([]andSet, 0, len(r.ExcludedRelations)+len(r.ExcludedParentRelations)+len(r.ExcludedIntersectionGroups))
	for _, ex := range r.ExcludedRelations {
		sets = append(sets, andSet{ex: true})
	}
	for _, ep := range r.ExcludedParentRelations {
		sets = append(sets, andSet{ttuToken(ep): true})
	}
	for _, g := range r.ExcludedIntersectionGroups {
		sets = append(sets, intersectionMembers(g))
	}
	return sets
}

// intersectionGrantSets expands one grant intersection group into andSets. A
// self-reference (`this`) is the relation's direct subject types, which are an
// OR — so the group distributes into one andSet per subject type.
func intersectionGrantSets(g IntersectionGroup, r RelationDefinition) []andSet {
	base := andSet{}
	selfRef := false
	for _, rel := range g.Relations {
		if rel == r.Name {
			selfRef = true
			continue
		}
		base[intersectionMemberToken(rel, g)] = true
	}
	for _, p := range g.ParentRelations {
		base[ttuToken(p)] = true
	}

	if !selfRef || len(r.SubjectTypeRefs) == 0 {
		return []andSet{base}
	}
	out := make([]andSet, 0, len(r.SubjectTypeRefs))
	for _, ref := range r.SubjectTypeRefs {
		s := andSet{subjectTypeToken(ref): true}
		for k := range base {
			s[k] = true
		}
		out = append(out, s)
	}
	return out
}

// intersectionMembers returns the ANDed member tokens of an intersection group
// (used for exclusion groups, which do not self-reference).
func intersectionMembers(g IntersectionGroup) andSet {
	s := andSet{}
	for _, rel := range g.Relations {
		s[intersectionMemberToken(rel, g)] = true
	}
	for _, p := range g.ParentRelations {
		s[ttuToken(p)] = true
	}
	return s
}

// intersectionMemberToken renders one intersection member, folding in any
// per-member exclusion (`editor but not owner`).
func intersectionMemberToken(rel string, g IntersectionGroup) string {
	if excls := g.Exclusions[rel]; len(excls) > 0 {
		sorted := append([]string(nil), excls...)
		sort.Strings(sorted)
		return rel + " but not " + strings.Join(sorted, ",")
	}
	return rel
}

// renderAndSet formats an andSet: a bare token when singleton, else `(a and b)`.
func renderAndSet(s andSet) string {
	toks := make([]string, 0, len(s))
	for k := range s {
		toks = append(toks, k)
	}
	sort.Strings(toks)
	if len(toks) == 1 {
		return toks[0]
	}
	return "(" + strings.Join(toks, " and ") + ")"
}

func subjectTypeToken(ref SubjectTypeRef) string {
	switch {
	case ref.Wildcard:
		return "[" + ref.Type + ":*]"
	case ref.Relation != "":
		return "[" + ref.Type + "#" + ref.Relation + "]"
	default:
		return "[" + ref.Type + "]"
	}
}

func ttuToken(p ParentRelationCheck) string {
	return p.Relation + " from " + p.LinkingRelation
}

func indexTypes(types []TypeDefinition) map[string]TypeDefinition {
	m := make(map[string]TypeDefinition, len(types))
	for _, t := range types {
		m[t.Name] = t
	}
	return m
}

func indexRelations(t TypeDefinition) map[string]RelationDefinition {
	m := make(map[string]RelationDefinition, len(t.Relations))
	for _, r := range t.Relations {
		m[r.Name] = r
	}
	return m
}

// sortChanges orders changes deterministically: by type, then relation, then
// breaking-before-additive, then summary.
func sortChanges(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		if a.Class != b.Class {
			return a.Class == ClassBreaking // breaking first
		}
		return a.Summary < b.Summary
	})
}
