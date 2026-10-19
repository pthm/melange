package studio

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// modelOrder ranks types and relations by where they appear in the deployed
// DSL, so every list in the UI follows the model as its author wrote it.
//
// The parsed model can't supply this: OpenFGA keeps a type's relations in a
// protobuf map, so they come back alphabetical. Types keep their order, but
// relations are recovered from the DSL text, which is stored verbatim.
type modelOrder struct {
	types map[string]int
	rels  map[string]map[string]int // type → relation → rank
}

var (
	typeLine   = regexp.MustCompile(`^\s*(?:extend\s+)?type\s+([^\s#]+)`)
	defineLine = regexp.MustCompile(`^\s*define\s+([^\s:]+)\s*:`)
)

func newModelOrder(m *migrator.DeployedModel) modelOrder {
	o := modelOrder{types: map[string]int{}, rels: map[string]map[string]int{}}
	if m == nil {
		return o
	}
	cur := ""
	for line := range strings.SplitSeq(m.DSL, "\n") {
		if t := typeLine.FindStringSubmatch(line); t != nil {
			cur = t[1]
			if _, ok := o.types[cur]; !ok {
				o.types[cur] = len(o.types)
				o.rels[cur] = map[string]int{}
			}
			continue
		}
		if d := defineLine.FindStringSubmatch(line); d != nil && cur != "" {
			if _, ok := o.rels[cur][d[1]]; !ok {
				o.rels[cur][d[1]] = len(o.rels[cur])
			}
		}
	}
	// Types the DSL scan missed (unlikely) follow in parsed order.
	for _, t := range m.Types {
		if _, ok := o.types[t.Name]; !ok {
			o.types[t.Name] = len(o.types)
		}
	}
	return o
}

// typeRank orders types by the model; unknown types sort after all known ones.
func (o modelOrder) typeRank(t string) int {
	if r, ok := o.types[t]; ok {
		return r
	}
	return len(o.types)
}

func (o modelOrder) relRank(typ, rel string) int {
	if r, ok := o.rels[typ][rel]; ok {
		return r
	}
	return len(o.rels[typ])
}

// compareTypes is a sort comparator: model order, then name for unknowns.
func (o modelOrder) compareTypes(a, b string) int {
	return cmp.Or(cmp.Compare(o.typeRank(a), o.typeRank(b)), strings.Compare(a, b))
}

func (o modelOrder) compareRels(typ, a, b string) int {
	return cmp.Or(cmp.Compare(o.relRank(typ, a), o.relRank(typ, b)), strings.Compare(a, b))
}

// sortRelations puts each type's relations back in DSL order, in place.
func (o modelOrder) sortRelations(m *migrator.DeployedModel) {
	for i := range m.Types {
		t := &m.Types[i]
		slices.SortStableFunc(t.Relations, func(a, b schema.RelationDefinition) int { return o.compareRels(t.Name, a.Name, b.Name) })
	}
}
