package sqlgen

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Exposure selects the SQL API generated for a model: which relations the
// public dispatchers answer for, and which function families are generated.
// The zero value is the default: every relation, every family.
//
// Exposure shapes the API surface; it is not an access-control boundary.
// A relation that is not exposed but that an exposed relation resolves
// through still gets its functions (see the installed sets below), and those
// functions and the *_internal dispatchers stay callable.
//
// Generation installs, from the exposed relations E and Reach (every relation
// E references, transitively, over relationReferences, E included):
//   - check_<t>_<r> (and _nw): Reach. Check is always generated.
//   - list_<t>_<r>_obj and accessible_<t>_<r>: Reach, when list_objects,
//     accessible or parent_key is enabled. The three select from each other.
//   - list_<t>_<r>_sub: Reach, when list_subjects is enabled.
//   - expand_<t>_<r>: Reach, when expand is enabled. Clients chase expand
//     pointers through expand_permission, so it answers for all of Reach.
//   - explain_<t>_<r>: Reach, when explain is enabled.
//   - accessible_<t>_<r>_by_<link>: E, when parent_key is enabled.
//
// The public dispatchers answer for the exposed relations of an enabled
// family and raise SQLSTATE M2003 for any other relation the model defines.
// Relations the model does not define keep the unknown-pair result (deny,
// empty set or sentinel).
type Exposure struct {
	Relations RelationPatterns `json:"relations"`
	Functions FunctionFamilies `json:"functions"`
}

// RelationPatterns selects the exposed relations. A relation is exposed when
// it matches an Include pattern (every relation when Include is empty) and no
// Exclude pattern.
//
// A pattern is "relation", matching that relation on every type, or
// "type#relation". '*' matches any run of characters and '?' one character,
// within one part: neither crosses '#', which cannot occur in OpenFGA type
// or relation names ('.' can, so it is not a separator).
type RelationPatterns struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// FunctionFamilies enables the optional function families. A nil field means
// enabled. Check functions are always generated.
type FunctionFamilies struct {
	ListObjects  *bool `json:"list_objects,omitempty"`
	ListSubjects *bool `json:"list_subjects,omitempty"`
	Accessible   *bool `json:"accessible,omitempty"`
	ParentKey    *bool `json:"parent_key,omitempty"`
	Expand       *bool `json:"expand,omitempty"`
	Explain      *bool `json:"explain,omitempty"`
}

// enabled reports whether a family flag is on (nil means on).
func enabled(flag *bool) bool { return flag == nil || *flag }

// NotExposedErrCode is the SQLSTATE a public dispatcher raises for a relation
// the model defines but the exposure does not expose.
const NotExposedErrCode = "M2003"

// UnexposedFunctionName is the raising helper the LANGUAGE sql public
// dispatchers call for a relation that is not exposed. It is generated only
// when the exposure leaves something out.
const UnexposedFunctionName = "check_permission_unexposed"

// Validate reports the first invalid pattern.
func (e Exposure) Validate() error {
	for _, list := range []struct {
		name     string
		patterns []string
	}{{"include", e.Relations.Include}, {"exclude", e.Relations.Exclude}} {
		for _, p := range list.patterns {
			if err := ValidateRelationPattern(p); err != nil {
				return fmt.Errorf("relations.%s: %w", list.name, err)
			}
		}
	}
	return nil
}

// Normalized returns e in canonical form, so that two exposures that select
// the same API for every model compare equal: patterns sorted and
// deduplicated, an Include that matches every relation dropped, and enabled
// family flags set to nil. The default exposure normalizes to the zero
// value. It does not resolve e against a model, so an Include that happens
// to name every relation of one model stays as written.
func (e Exposure) Normalized() Exposure {
	var out Exposure
	if !slices.ContainsFunc(e.Relations.Include, matchesEverything) {
		out.Relations.Include = sortedUnique(e.Relations.Include)
	}
	out.Relations.Exclude = sortedUnique(e.Relations.Exclude)
	out.Functions = FunctionFamilies{
		ListObjects:  normalizedFlag(e.Functions.ListObjects),
		ListSubjects: normalizedFlag(e.Functions.ListSubjects),
		Accessible:   normalizedFlag(e.Functions.Accessible),
		ParentKey:    normalizedFlag(e.Functions.ParentKey),
		Expand:       normalizedFlag(e.Functions.Expand),
		Explain:      normalizedFlag(e.Functions.Explain),
	}
	return out
}

// normalizedFlag returns a family flag in canonical form: nil when enabled,
// a pointer to false when disabled.
func normalizedFlag(flag *bool) *bool {
	if enabled(flag) {
		return nil
	}
	off := false
	return &off
}

// IsDefault reports whether e normalizes to the default exposure.
func (e Exposure) IsDefault() bool {
	n := e.Normalized()
	return len(n.Relations.Include) == 0 && len(n.Relations.Exclude) == 0 && n.Functions == FunctionFamilies{}
}

// matchesEverything reports whether every part of the pattern is only '*',
// so it matches every relation.
func matchesEverything(p string) bool {
	for _, part := range strings.Split(p, "#") {
		if part == "" || strings.Trim(part, "*") != "" {
			return false
		}
	}
	return true
}

// sortedUnique returns a sorted copy of patterns without duplicates, or nil
// when there are none.
func sortedUnique(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := slices.Clone(patterns)
	slices.Sort(out)
	return slices.Compact(out)
}

// ValidateRelationPattern reports whether p is a valid relation pattern.
func ValidateRelationPattern(p string) error {
	if p == "" {
		return fmt.Errorf("empty relation pattern")
	}
	parts := strings.Split(p, "#")
	if len(parts) > 2 {
		return fmt.Errorf("relation pattern %q: more than one '#'", p)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("relation pattern %q: empty type or relation", p)
		}
		if i := strings.IndexFunc(part, func(r rune) bool {
			return unicode.IsSpace(r) || r == ':' || r == '@'
		}); i >= 0 {
			return fmt.Errorf("relation pattern %q: %q cannot occur in a type or relation name", p, part[i:i+1])
		}
	}
	return nil
}

// patternMatches reports whether the valid pattern p matches the relation.
func patternMatches(p, objectType, relation string) bool {
	typePattern, relationPattern, qualified := strings.Cut(p, "#")
	if !qualified {
		return globMatch(p, relation)
	}
	return globMatch(typePattern, objectType) && globMatch(relationPattern, relation)
}

// globMatch matches name against a pattern in which '*' matches any run of
// characters and '?' exactly one; every other character matches itself.
func globMatch(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	// star is the position after the last '*' seen and mark the name position
	// it currently absorbs up to: on a mismatch, the star absorbs one more.
	star, mark := -1, 0
	i, j := 0, 0
	for j < len(n) {
		switch {
		case i < len(p) && (p[i] == '?' || p[i] == n[j]):
			i++
			j++
		case i < len(p) && p[i] == '*':
			i++
			star, mark = i, j
		case star >= 0:
			mark++
			i, j = star, mark
		default:
			return false
		}
	}
	for i < len(p) && p[i] == '*' {
		i++
	}
	return i == len(p)
}

// Exposes reports whether the relation is exposed. Patterns are assumed
// valid (see Validate).
func (e Exposure) Exposes(objectType, relation string) bool {
	matchesAny := func(patterns []string) bool {
		for _, p := range patterns {
			if patternMatches(p, objectType, relation) {
				return true
			}
		}
		return false
	}
	if len(e.Relations.Include) > 0 && !matchesAny(e.Relations.Include) {
		return false
	}
	return !matchesAny(e.Relations.Exclude)
}

// UnmatchedPatterns returns the include and exclude patterns that match no
// relation in analyses, in configuration order.
func (e Exposure) UnmatchedPatterns(analyses []RelationAnalysis) []string {
	var out []string
	for _, patterns := range [][]string{e.Relations.Include, e.Relations.Exclude} {
		for _, p := range patterns {
			matched := false
			for _, a := range analyses {
				if patternMatches(p, a.ObjectType, a.Relation) {
					matched = true
					break
				}
			}
			if !matched {
				out = append(out, p)
			}
		}
	}
	return out
}

// apiFamily is a public dispatcher's function family.
type apiFamily int

const (
	familyCheck apiFamily = iota
	familyListObjects
	familyListSubjects
	familyAccessible
	familyExplain
	familyExpand
)

// relationKey is a (type, relation) pair the model defines.
type relationKey struct{ ObjectType, Relation string }

// exposureClosure is an Exposure resolved against a model: the exposed
// relations, their Reach, and the enabled families. A nil closure is the
// default exposure, under which everything is installed and public and no
// raise arm is rendered, so generation is unchanged.
//
// Only emission and dispatcher arms read it. Every decision a function body
// depends on (composition, _nw routing, complexity, eligibility, parent-key
// names) is made over the full model, so bodies do not depend on the
// exposure.
type exposureClosure struct {
	defined []relationKey // every relation the model defines, in analysis order
	// exposed and reach are keyed like buildAnalysisLookup: "type.relation".
	exposed map[string]bool
	reach   map[string]bool

	listObjects, listSubjects, accessible, parentKey, expand, explain bool
}

// newExposureClosure resolves e against analyses. It returns nil when e
// leaves nothing out for this model.
func newExposureClosure(analyses []RelationAnalysis, e Exposure) *exposureClosure {
	c := &exposureClosure{
		exposed:      make(map[string]bool),
		listObjects:  enabled(e.Functions.ListObjects),
		listSubjects: enabled(e.Functions.ListSubjects),
		accessible:   enabled(e.Functions.Accessible),
		parentKey:    enabled(e.Functions.ParentKey),
		expand:       enabled(e.Functions.Expand),
		explain:      enabled(e.Functions.Explain),
	}
	restricted := !c.listObjects || !c.listSubjects || !c.accessible || !c.parentKey || !c.expand || !c.explain
	var queue []string
	for _, a := range analyses {
		c.defined = append(c.defined, relationKey{a.ObjectType, a.Relation})
		if e.Exposes(a.ObjectType, a.Relation) {
			c.exposed[a.ObjectType+"."+a.Relation] = true
			queue = append(queue, a.ObjectType+"."+a.Relation)
		} else {
			restricted = true
		}
	}
	if !restricted || len(analyses) == 0 {
		return nil
	}

	// Reach: forward BFS over relationReferences from the exposed relations.
	// A referenced key without an analysis has no edges of its own.
	lookup := buildAnalysisLookup(analyses)
	c.reach = make(map[string]bool, len(lookup))
	for _, k := range queue {
		c.reach[k] = true
	}
	for len(queue) > 0 {
		a := lookup[queue[0]]
		queue = queue[1:]
		if a == nil {
			continue
		}
		for _, ref := range relationReferences(a) {
			if !c.reach[ref] {
				c.reach[ref] = true
				queue = append(queue, ref)
			}
		}
	}
	return c
}

// installsCheck reports whether check_<t>_<r> (and its _nw variant) is
// generated, given the relation is CheckAllowed.
func (c *exposureClosure) installsCheck(objectType, relation string) bool {
	return c == nil || c.reach[objectType+"."+relation]
}

// installsListObjects reports whether list_<t>_<r>_obj and accessible_<t>_<r>
// are generated, given the relation is ListAllowed.
func (c *exposureClosure) installsListObjects(objectType, relation string) bool {
	return c == nil || ((c.listObjects || c.accessible || c.parentKey) && c.reach[objectType+"."+relation])
}

// installsListSubjects reports whether list_<t>_<r>_sub is generated, given
// the relation is ListAllowed.
func (c *exposureClosure) installsListSubjects(objectType, relation string) bool {
	return c == nil || (c.listSubjects && c.reach[objectType+"."+relation])
}

// installsExpand reports whether expand_<t>_<r> is generated, given the
// relation is expand-eligible.
func (c *exposureClosure) installsExpand(objectType, relation string) bool {
	return c == nil || (c.expand && c.reach[objectType+"."+relation])
}

// installsExplain reports whether explain_<t>_<r> is generated, given the
// relation is explain-eligible.
func (c *exposureClosure) installsExplain(objectType, relation string) bool {
	return c == nil || (c.explain && c.reach[objectType+"."+relation])
}

// installsParentKey reports whether the relation's parent-key function is
// generated, given it has one.
func (c *exposureClosure) installsParentKey(objectType, relation string) bool {
	return c == nil || (c.parentKey && c.exposed[objectType+"."+relation])
}

// public reports whether family's public dispatcher answers for the
// relation instead of raising.
func (c *exposureClosure) public(family apiFamily, objectType, relation string) bool {
	if c == nil {
		return true
	}
	k := objectType + "." + relation
	switch family {
	case familyListObjects:
		return c.listObjects && c.exposed[k]
	case familyListSubjects:
		return c.listSubjects && c.exposed[k]
	case familyAccessible:
		return c.accessible && c.exposed[k]
	case familyExplain:
		return c.explain && c.exposed[k]
	case familyExpand:
		return c.expand && c.reach[k]
	default:
		return c.exposed[k]
	}
}

// ExpandPublic reports whether the expand dispatcher answers for a relation
// under e rather than raising: every relation under the default exposure.
func ExpandPublic(analyses []RelationAnalysis, e Exposure) func(objectType, relation string) bool {
	return NewPublicAPI(analyses, e).Expand
}

// PublicAPI reports which relations each public dispatcher family answers
// for under an exposure, for tools that call the dispatchers for a relation
// the user names. For a relation the model defines, false means the
// dispatcher raises NotExposedErrCode. Under the default exposure every
// method reports true.
type PublicAPI struct{ c *exposureClosure }

// NewPublicAPI resolves e against analyses.
func NewPublicAPI(analyses []RelationAnalysis, e Exposure) PublicAPI {
	return PublicAPI{newExposureClosure(analyses, e)}
}

// Check covers check_permission, check_permission_nw and check_permission_bulk.
func (p PublicAPI) Check(objectType, relation string) bool {
	return p.c.public(familyCheck, objectType, relation)
}

// ListObjects covers list_accessible_objects.
func (p PublicAPI) ListObjects(objectType, relation string) bool {
	return p.c.public(familyListObjects, objectType, relation)
}

// ListSubjects covers list_accessible_subjects.
func (p PublicAPI) ListSubjects(objectType, relation string) bool {
	return p.c.public(familyListSubjects, objectType, relation)
}

// Explain covers explain_permission.
func (p PublicAPI) Explain(objectType, relation string) bool {
	return p.c.public(familyExplain, objectType, relation)
}

// Expand covers expand_permission, which also answers for the relations an
// exposed relation's expand pointers lead to.
func (p PublicAPI) Expand(objectType, relation string) bool {
	return p.c.public(familyExpand, objectType, relation)
}

// unexposed returns the relations the model defines that family's public
// dispatcher raises for, in analysis order. nil under the default exposure.
func (c *exposureClosure) unexposed(family apiFamily) []relationKey {
	if c == nil {
		return nil
	}
	var out []relationKey
	for _, k := range c.defined {
		if !c.public(family, k.ObjectType, k.Relation) {
			out = append(out, k)
		}
	}
	return out
}

// raiseNotExposed is the PL/pgSQL statement a public dispatcher runs for a
// relation it does not expose.
var raiseNotExposed = RawStmt{SQLText: `RAISE EXCEPTION 'relation "%" on type "%" is not exposed', p_relation, p_object_type USING ERRCODE = '` + NotExposedErrCode + `';`}

// renderUnexposedFunction renders the raising helper the LANGUAGE sql public
// dispatchers call. It is PL/pgSQL because a LANGUAGE sql body cannot raise,
// and STABLE so the wrappers that call it stay inlinable.
func renderUnexposedFunction(databaseSchema string) string {
	fn := PlpgsqlFunction{
		Schema: databaseSchema,
		Name:   UnexposedFunctionName,
		Args: []FuncArg{
			{Name: "p_object_type", Type: "TEXT"},
			{Name: "p_relation", Type: "TEXT"},
		},
		Returns: "INTEGER",
		Body:    []Stmt{raiseNotExposed},
		Header: []string{
			"Raises " + NotExposedErrCode + " for a relation the model defines but does not expose",
			"Called by the public dispatchers' guards",
		},
		NoSearchPath: true,
	}
	return fn.SQL()
}

// unexposedGuard returns a boolean expression that is true when
// (objectType, relation) is one of keys: a CASE on the type, then an IN over
// that type's relations, so a call costs O(#types + #relations of the type)
// like the dispatchers' IF-chains.
func unexposedGuard(objectType, relation Expr, keys []relationKey) Expr {
	var order []string
	byType := make(map[string][]string)
	for _, k := range keys {
		if _, seen := byType[k.ObjectType]; !seen {
			order = append(order, k.ObjectType)
		}
		byType[k.ObjectType] = append(byType[k.ObjectType], k.Relation)
	}
	whens := make([]CaseWhen, 0, len(order))
	for _, ot := range order {
		whens = append(whens, CaseWhen{
			Cond:   Eq{Left: objectType, Right: Lit(ot)},
			Result: In{Expr: relation, Values: byType[ot]},
		})
	}
	return CaseExpr{Whens: whens, Else: Bool(false)}
}

// guardedPublicBody returns a LANGUAGE sql public dispatcher's body: SELECT
// result, or, when keys is non-empty, a CASE that calls the raising helper
// for those relations and returns result for every other. jsonb wraps the
// helper's INTEGER for a JSONB result.
func guardedPublicBody(databaseSchema string, keys []relationKey, result string, jsonb bool) Raw {
	if len(keys) == 0 {
		return Raw("SELECT " + result)
	}
	raise := Func{Schema: databaseSchema, Name: UnexposedFunctionName, Args: []Expr{ObjectType, Raw("p_relation")}}.SQL()
	if jsonb {
		raise = "to_jsonb(" + raise + ")"
	}
	return Raw("SELECT CASE WHEN " + unexposedGuard(ObjectType, Raw("p_relation"), keys).SQL() + "\n        THEN " + raise + "\n        ELSE " + result + "\n    END")
}
