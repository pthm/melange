package clientgen

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/schema"
)

// Model is what a generator emits for a schema under a Config.
type Model struct {
	// ObjectTypes is every type of the schema, sorted.
	ObjectTypes []string

	// Relations is the relation names selected on at least one type, sorted.
	Relations []string

	// Accessible is the accessible_<t>_<r> functions installed for the
	// selected relations, sorted by type and relation.
	Accessible []compiler.AccessibleFunction

	// AccessibleByParent is the parent-key accessible_<t>_<r>_by_<link>
	// functions installed for the selected relations, sorted by type,
	// relation and link.
	AccessibleByParent []compiler.AccessibleFunction
}

// Resolve validates types as a migration does, compiles them under
// cfg.Exposure, and selects what the generators emit: every type, the
// relations cfg selects, and the accessible functions installed for them.
// A nil cfg is the zero Config.
//
// Names are computed over the whole model and then filtered, as migrate
// does, so the selection never renames a function.
func Resolve(types []schema.TypeDefinition, cfg *Config) (*Model, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	if err := schema.DetectCycles(types); err != nil {
		return nil, err
	}
	var exposure compiler.Exposure
	if cfg.Exposure != nil {
		exposure = *cfg.Exposure
	}
	if err := exposure.Validate(); err != nil {
		return nil, fmt.Errorf("invalid exposure: %w", err)
	}
	selector := compiler.Exposure{Relations: exposure.Relations}
	if cfg.Relations != nil {
		selector.Relations = *cfg.Relations
		if err := selector.Validate(); err != nil {
			return nil, fmt.Errorf("invalid client relations: %w", err)
		}
	}
	selected := func(objectType, relation string) bool {
		return selector.Exposes(objectType, relation) && strings.HasPrefix(relation, cfg.RelationFilter)
	}

	// Compile as migrate does, so a model migrate cannot install gets no
	// names either.
	closure := schema.ComputeRelationClosure(types)
	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closure))
	inline := compiler.BuildInlineSQLData(closure, analyses)
	opts := compiler.GenerateSQLOptions{Exposure: exposure}
	if _, err := compiler.GenerateSQLWithOptions(analyses, inline, "", opts); err != nil {
		return nil, fmt.Errorf("generating check SQL: %w", err)
	}
	if _, err := compiler.GenerateListSQLWithOptions(analyses, inline, "", opts); err != nil {
		return nil, fmt.Errorf("generating list SQL: %w", err)
	}
	accessible, err := compiler.AccessibleFunctions(analyses, exposure)
	if err != nil {
		return nil, err
	}

	m := &Model{}
	relSet := make(map[string]bool)
	for _, t := range types {
		m.ObjectTypes = append(m.ObjectTypes, t.Name)
		for _, r := range t.Relations {
			if selected(t.Name, r.Name) {
				relSet[r.Name] = true
			}
		}
	}
	slices.Sort(m.ObjectTypes)
	m.Relations = slices.Sorted(maps.Keys(relSet))

	// Distinct relations can share a SQL name (type a_b relation c and type
	// a relation b_c, or names that differ only in case): migrate installs
	// one function under it, so the client cannot name both.
	owners := make(map[string]compiler.AccessibleFunction)
	for _, f := range accessible {
		if !selected(f.ObjectType, f.Relation) {
			continue
		}
		if prev, ok := owners[f.Name]; ok {
			return nil, fmt.Errorf("%s and %s both map to the SQL function %s; rename one of them",
				describeAccessible(prev), describeAccessible(f), f.Name)
		}
		owners[f.Name] = f
		if f.Link == "" {
			m.Accessible = append(m.Accessible, f)
		} else {
			m.AccessibleByParent = append(m.AccessibleByParent, f)
		}
	}
	return m, nil
}

// describeAccessible names the relation an accessible function is for.
func describeAccessible(f compiler.AccessibleFunction) string {
	if f.Link == "" {
		return fmt.Sprintf("%s#%s", f.ObjectType, f.Relation)
	}
	return fmt.Sprintf("%s#%s by %s", f.ObjectType, f.Relation, f.Link)
}

// Identifiers records the top-level identifiers of a generated file, so a
// generator reports two schema names that map to one identifier instead of
// emitting code that does not compile.
type Identifiers struct {
	valid func(string) bool
	owner map[string]string
}

// NewIdentifiers returns an empty set whose identifiers must satisfy valid.
func NewIdentifiers(valid func(string) bool) *Identifiers {
	return &Identifiers{valid: valid, owner: make(map[string]string)}
}

// Declare records ident for what, the schema element it is generated from.
// It fails when ident is not a valid identifier or already names something
// else.
func (ids *Identifiers) Declare(ident, what string) error {
	if !ids.valid(ident) {
		return fmt.Errorf("%s would be named %q, which is not a valid identifier", what, ident)
	}
	if prev, ok := ids.owner[ident]; ok {
		return fmt.Errorf("%s and %s would both be named %s", prev, what, ident)
	}
	ids.owner[ident] = what
	return nil
}
