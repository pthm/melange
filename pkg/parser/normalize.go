package parser

import (
	"fmt"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// maxNormalTerms bounds disjunctive-normal-form expansion. Distributing
// intersections over unions (and negations over intersections) multiplies
// terms; a real schema stays far below this, and a pathological one is
// rejected instead of compiling to an enormous function.
const maxNormalTerms = 64

type literalKind int

const (
	litRelation literalKind = iota // computed userset: `editor`
	litThis                        // direct assignment on the relation being defined
	litTTU                         // tuple-to-userset: `viewer from parent`
)

type literal struct {
	kind    literalKind
	rel     string // relation (or TTU's computed relation)
	link    string // TTU tupleset relation
	negated bool
}

// term is a conjunction of literals; a normal form is a disjunction of terms.
type term []literal

// normalize rewrites a userset into disjunctive normal form over relation,
// direct and TTU literals, each possibly negated. Every OpenFGA userset node
// is handled, so nothing is dropped: a shape the flat schema cannot express
// is rejected later by toGroup, never silently widened or narrowed.
func normalize(us *openfgav1.Userset) ([]term, error) {
	switch v := us.GetUserset().(type) {
	case *openfgav1.Userset_This:
		return []term{{{kind: litThis}}}, nil
	case *openfgav1.Userset_ComputedUserset:
		return []term{{{kind: litRelation, rel: v.ComputedUserset.GetRelation()}}}, nil
	case *openfgav1.Userset_TupleToUserset:
		return []term{{{
			kind: litTTU,
			rel:  v.TupleToUserset.GetComputedUserset().GetRelation(),
			link: v.TupleToUserset.GetTupleset().GetRelation(),
		}}}, nil
	case *openfgav1.Userset_Union:
		var out []term
		for _, child := range v.Union.GetChild() {
			terms, err := normalize(child)
			if err != nil {
				return nil, err
			}
			out = append(out, terms...)
		}
		return out, checkTermCount(out)
	case *openfgav1.Userset_Intersection:
		out := []term{{}}
		for _, child := range v.Intersection.GetChild() {
			terms, err := normalize(child)
			if err != nil {
				return nil, err
			}
			if out, err = product(out, terms); err != nil {
				return nil, err
			}
		}
		return out, nil
	case *openfgav1.Userset_Difference:
		base, err := normalize(v.Difference.GetBase())
		if err != nil {
			return nil, err
		}
		sub, err := normalize(v.Difference.GetSubtract())
		if err != nil {
			return nil, err
		}
		notSub, err := negate(sub)
		if err != nil {
			return nil, err
		}
		return product(base, notSub)
	default:
		return nil, fmt.Errorf("%w: unsupported userset node %T", melange.ErrInvalidSchema, v)
	}
}

// negate applies De Morgan: not(T1 or T2) = not T1 and not T2, and
// not(l1 and l2) = (not l1) or (not l2).
func negate(terms []term) ([]term, error) {
	out := []term{{}}
	for _, t := range terms {
		alternatives := make([]term, 0, len(t))
		for _, l := range t {
			l.negated = !l.negated
			alternatives = append(alternatives, term{l})
		}
		var err error
		if out, err = product(out, alternatives); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// product distributes AND over OR: (A or B) and (C or D) = AC or AD or BC or BD.
func product(left, right []term) ([]term, error) {
	out := make([]term, 0, len(left)*len(right))
	for _, l := range left {
		for _, r := range right {
			out = append(out, append(append(term{}, l...), r...))
		}
	}
	return out, checkTermCount(out)
}

func checkTermCount(terms []term) error {
	if len(terms) > maxNormalTerms {
		return fmt.Errorf("%w: expression expands to %d alternatives (limit %d); split it into named relations",
			melange.ErrInvalidSchema, len(terms), maxNormalTerms)
	}
	return nil
}

// toGroup converts one conjunction into an IntersectionGroup. Positive
// literals become members. A negated relation is attached as an exclusion to
// the nearest preceding member (the base of the `but not` it came from), or
// the nearest following one; a term is a conjunction, so any member is
// semantically equivalent. Negated TTU or direct literals have no flat
// encoding and are rejected. allowThis is false for subtract groups, whose
// codegen has no direct-assignment part.
func toGroup(t term, relationName string, allowThis bool) (schema.IntersectionGroup, error) {
	var g schema.IntersectionGroup
	memberAt := make([]string, len(t)) // relation key of positive literal i, "" otherwise
	for i, l := range t {
		if l.negated {
			continue
		}
		switch l.kind {
		case litRelation:
			memberAt[i] = l.rel
			g.Relations = append(g.Relations, l.rel)
		case litThis:
			if !allowThis {
				return schema.IntersectionGroup{}, unsupportedShape(relationName, "uses a direct assignment inside an excluded expression")
			}
			memberAt[i] = relationName
			g.Relations = append(g.Relations, relationName)
		case litTTU:
			g.ParentRelations = append(g.ParentRelations, schema.ParentRelationCheck{Relation: l.rel, LinkingRelation: l.link})
		}
	}
	for i, l := range t {
		if !l.negated {
			continue
		}
		if l.kind != litRelation {
			return schema.IntersectionGroup{}, unsupportedShape(relationName, "excludes a tuple-to-userset or direct assignment inside a compound expression")
		}
		anchor := nearestMember(memberAt, i)
		if anchor == "" {
			return schema.IntersectionGroup{}, unsupportedShape(relationName, "has an exclusion whose base is only a tuple-to-userset")
		}
		if g.Exclusions == nil {
			g.Exclusions = make(map[string][]string)
		}
		g.Exclusions[anchor] = append(g.Exclusions[anchor], l.rel)
	}
	return g, nil
}

func nearestMember(memberAt []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		if memberAt[j] != "" {
			return memberAt[j]
		}
	}
	for j := i + 1; j < len(memberAt); j++ {
		if memberAt[j] != "" {
			return memberAt[j]
		}
	}
	return ""
}

func unsupportedShape(relationName, what string) error {
	return fmt.Errorf("%w: relation %q %s, which melange cannot compile exactly; move the sub-expression into its own named relation. See %s",
		melange.ErrInvalidSchema, relationName, what, compatibilityDocsURL)
}
