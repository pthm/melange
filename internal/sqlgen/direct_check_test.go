package sqlgen

import (
	"maps"
	"regexp"
	"strings"
	"testing"
)

// directCheckModel has a literal-type userset hop (group#member), a
// single-type TTU (folder.parent: [folder]), a multi-type TTU
// (doc.parent: [folder, project]) and a wildcard that gives doc.viewer a _nw
// variant.
const directCheckModel = `model
  schema 1.1

type user

type group
  relations
    define member: [user, group#member]

type project
  relations
    define viewer: [user]

type folder
  relations
    define parent: [folder]
    define viewer: [user, group#member] or viewer from parent

type doc
  relations
    define parent: [folder, project]
    define public: [user:*]
    define viewer: public or viewer from parent
`

func directCheckGraph(t *testing.T) *relationGraph {
	t.Helper()
	m := mustExposureModel(t, directCheckModel)
	return newRelationGraph(buildAnalysisLookup(m.analyses))
}

func TestRelationGraph_CheckFunction(t *testing.T) {
	g := directCheckGraph(t)
	// gBlocked is the same model with group.member not CheckAllowed.
	blocked := *g.lookup["group.member"]
	blocked.Capabilities.CheckAllowed = false
	lookup := maps.Clone(g.lookup)
	lookup["group.member"] = &blocked
	gBlocked := newRelationGraph(lookup)

	for _, tc := range []struct {
		name               string
		g                  *relationGraph
		objectType, rel    string
		noWildcard, wantOK bool
		want               string
	}{
		{name: "nil graph", g: nil, objectType: "folder", rel: "viewer"},
		{name: "unknown pair", g: g, objectType: "project", rel: "parent"},
		{name: "not CheckAllowed", g: gBlocked, objectType: "group", rel: "member"},
		{name: "base function", g: g, objectType: "folder", rel: "viewer", wantOK: true, want: "check_folder_viewer"},
		{name: "_nw variant emitted", g: g, objectType: "doc", rel: "viewer", noWildcard: true, wantOK: true, want: "check_doc_viewer_nw"},
		{name: "_nw falls back to the base function", g: g, objectType: "folder", rel: "viewer", noWildcard: true, wantOK: true, want: "check_folder_viewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.g.checkFunction(tc.objectType, tc.rel, tc.noWildcard)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("checkFunction(%s, %s, %v) = %q, %v; want %q, %v", tc.objectType, tc.rel, tc.noWildcard, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestCheckPermission_Routing(t *testing.T) {
	g := directCheckGraph(t)
	linkType := Col{Table: "link", Column: "subject_type"}
	linkID := Col{Table: "link", Column: "subject_id"}
	check := func(mod func(*CheckPermission)) string {
		c := CheckPermission{
			Schema:      "authz",
			Subject:     SubjectParams(),
			Relation:    "viewer",
			Object:      LiteralObject("folder", ObjectID),
			ExpectAllow: true,
			Graph:       g,
		}
		mod(&c)
		return c.SQL()
	}
	for _, tc := range []struct {
		name string
		mod  func(*CheckPermission)
		want string
	}{
		{
			"literal type", func(*CheckPermission) {},
			`"authz"."check_folder_viewer"(p_subject_type, p_subject_id, p_object_id) = 1`,
		},
		{
			"visited passed on", func(c *CheckPermission) { c.Visited = Visited },
			`"authz"."check_folder_viewer"(p_subject_type, p_subject_id, p_object_id, p_visited) = 1`,
		},
		{
			"empty visited omitted", func(c *CheckPermission) { c.Visited = EmptyArray{} },
			`"authz"."check_folder_viewer"(p_subject_type, p_subject_id, p_object_id) = 1`,
		},
		{
			"deny", func(c *CheckPermission) { c.ExpectAllow = false },
			`"authz"."check_folder_viewer"(p_subject_type, p_subject_id, p_object_id) = 0`,
		},
		{
			"no graph keeps the dispatcher", func(c *CheckPermission) { c.Graph = nil },
			`"authz"."check_permission_internal"(p_subject_type, p_subject_id, 'viewer', 'folder', p_object_id, ARRAY[]::TEXT[]) = 1`,
		},
		{
			"unknown pair keeps the dispatcher", func(c *CheckPermission) { c.Relation = "owner" },
			`"authz"."check_permission_internal"(p_subject_type, p_subject_id, 'owner', 'folder', p_object_id, ARRAY[]::TEXT[]) = 1`,
		},
		{
			"dynamic type keeps the dispatcher", func(c *CheckPermission) { c.Object = ObjectRef{Type: linkType, ID: linkID} },
			`"authz"."check_permission_internal"(p_subject_type, p_subject_id, 'viewer', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1`,
		},
		{"filtered dynamic type", func(c *CheckPermission) {
			c.Object = ObjectRef{Type: linkType, ID: linkID}
			c.FilteredType = "folder"
		}, `"authz"."check_folder_viewer"(p_subject_type, p_subject_id, link.subject_id) = 1`},
		{"no wildcard", func(c *CheckPermission) {
			c.Object = LiteralObject("doc", ObjectID)
			c.NoWildcard = true
		}, `"authz"."check_doc_viewer_nw"(p_subject_type, p_subject_id, p_object_id) = 1`},
		{
			"no wildcard without a _nw variant", func(c *CheckPermission) { c.NoWildcard = true },
			`"authz"."check_folder_viewer"(p_subject_type, p_subject_id, p_object_id) = 1`,
		},
		{"no wildcard dispatcher", func(c *CheckPermission) {
			c.NoWildcard = true
			c.Graph = nil
		}, `"authz"."check_permission_nw_internal"(p_subject_type, p_subject_id, 'viewer', 'folder', p_object_id, ARRAY[]::TEXT[]) = 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := check(tc.mod); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// depthGuardSQL is checkDepthGuard as rendered at the top of a body.
const depthGuardSQL = "BEGIN\n    IF array_length(p_visited, 1) >= 26 THEN\n        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';\n    END IF;\n"

// TestCheckFunctions_DepthGuardAndCost checks that every check function and
// _nw variant starts with the dispatcher's depth guard, ahead of the cycle
// check in recursive ones, and carries the dispatcher's COST 1000, so a direct
// call raises and plans as a call through check_permission_internal did.
func TestCheckFunctions_DepthGuardAndCost(t *testing.T) {
	for _, m := range exposureCorpus(t) {
		o := generateExposed(t, m, Exposure{})
		for _, fn := range append(append([]string(nil), o.gen.Functions...), o.gen.NoWildcardFunctions...) {
			name := created(fn)
			if !strings.Contains(fn, depthGuardSQL) {
				t.Errorf("%s: %v does not start with the depth guard:\n%s", m.name, name, fn)
				continue
			}
			if cycle := strings.Index(fn, "v_key = ANY(p_visited)"); cycle >= 0 && cycle < strings.Index(fn, depthGuardSQL) {
				t.Errorf("%s: %v checks for a cycle before the depth guard", m.name, name)
			}
			if !strings.Contains(fn, " COST 1000\n") {
				t.Errorf("%s: %v is not declared COST 1000", m.name, name)
			}
		}
	}
}

// lineCall is a call in a generated body with the line it is on: a direct
// call and the type filter it relies on render on one line.
type lineCall struct {
	call
	line string
}

// lineCalls returns the schema-qualified calls in sql (comments excluded),
// each with its line.
func lineCalls(sql string) []lineCall {
	var out []lineCall
	for _, line := range strings.Split(withoutComments(sql), "\n") {
		for _, c := range calls(line) {
			out = append(out, lineCall{call: c, line: line})
		}
	}
	return out
}

// singleTypeFilter matches "<alias>.subject_type IN ('t')" and
// "<alias>.subject_type = 't'".
var singleTypeFilter = regexp.MustCompile(`(\w+)\.subject_type (?:IN \('([^']+)'\)|= '([^']+)')`)

// TestDirectCheck_NoAvoidableDispatcherCalls checks that every body call to an
// internal check dispatcher left in the generated SQL could not be made
// direct: its target pair has no check function, or its object type is a
// column the query does not restrict to a single type. The corpus covers the
// kitchen-sink and SaaS schemas, where only multi-type TTU calls remain.
func TestDirectCheck_NoAvoidableDispatcherCalls(t *testing.T) {
	remaining := map[string]int{}
	for _, m := range exposureCorpus(t) {
		o := generateExposed(t, m, Exposure{})
		g := newRelationGraph(buildAnalysisLookup(m.analyses))
		for _, nf := range o.named {
			for _, c := range lineCalls(nf.SQL) {
				if c.name != "check_permission_internal" && c.name != "check_permission_nw_internal" {
					continue
				}
				noWildcard := c.name == "check_permission_nw_internal"
				if len(c.args) != 6 {
					t.Errorf("%s: %s: cannot parse %s arguments %v", m.name, nf.Name, c.name, c.args)
					continue
				}
				relation, ok := literal(c.args[2])
				if !ok {
					t.Errorf("%s: %s calls %s with a non-literal relation %s", m.name, nf.Name, c.name, c.args[2])
					continue
				}
				var candidates []string
				if objectType, ok := literal(c.args[3]); ok {
					candidates = []string{objectType}
				} else {
					alias := strings.TrimSuffix(c.args[3], ".subject_type")
					for _, f := range singleTypeFilter.FindAllStringSubmatch(c.line, -1) {
						if f[1] == alias {
							candidates = append(candidates, f[2]+f[3])
						}
					}
				}
				for _, objectType := range candidates {
					if fn, ok := g.checkFunction(objectType, relation, noWildcard); ok {
						t.Errorf("%s: %s calls %s for (%s, %s), which could call %s directly:\n%s",
							m.name, nf.Name, c.name, c.args[3], relation, fn, c.line)
					}
				}
				remaining[m.name]++
			}
		}
	}
	// The kitchen sink keeps only folder's multi-type parent ([folder,
	// project]) on the dispatcher; SaaS has none.
	if got := remaining["kitchen_sink_schema.fga"]; got != 3 {
		t.Errorf("kitchen sink has %d dispatcher calls left in bodies, want 3", got)
	}
	if got := remaining["saas_schema.fga"]; got != 0 {
		t.Errorf("SaaS has %d dispatcher calls left in bodies, want 0", got)
	}
}

// columnObjectID matches a check function's object id argument when it is the
// subject id of a tuple alias, the shape a FilteredType call takes.
var columnObjectID = regexp.MustCompile(`^(\w+)\.subject_id$`)

// TestDirectCheck_ColumnObjectsAreTypeFiltered is the soundness side of
// TestDirectCheck_NoAvoidableDispatcherCalls. A direct call such as
// check_folder_viewer(…, link.subject_id, …) replaces a dispatcher call that
// routed on link.subject_type at run time, so it is only correct while the
// query keeps link.subject_type to that one type. Without the filter, a
// melange_tuples row with a type the model does not allow would be checked as
// an id of the called function's type. Every check function call whose object
// id is <alias>.subject_id must therefore share its line with a single-type
// filter on the same alias naming the function's object type.
func TestDirectCheck_ColumnObjectsAreTypeFiltered(t *testing.T) {
	filtered := map[string]int{}
	for _, m := range exposureCorpus(t) {
		o := generateExposed(t, m, Exposure{})
		objectTypes := make(map[string]string)
		for _, a := range m.analyses {
			objectTypes[functionName(a.ObjectType, a.Relation)] = a.ObjectType
			objectTypes[functionNameNoWildcard(a.ObjectType, a.Relation)] = a.ObjectType
		}
		for _, nf := range o.named {
			for _, c := range lineCalls(nf.SQL) {
				objectType, ok := objectTypes[c.name]
				if !ok || strings.HasPrefix(c.line, "CREATE OR REPLACE FUNCTION") {
					continue
				}
				if len(c.args) < 3 {
					t.Errorf("%s: %s: cannot parse %s arguments %v", m.name, nf.Name, c.name, c.args)
					continue
				}
				id := columnObjectID.FindStringSubmatch(c.args[2])
				if id == nil {
					continue
				}
				found := false
				for _, f := range singleTypeFilter.FindAllStringSubmatch(c.line, -1) {
					if f[1] == id[1] && f[2]+f[3] == objectType {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("%s: %s calls %s on %s without restricting %s.subject_type to '%s':\n%s",
						m.name, nf.Name, c.name, c.args[2], id[1], objectType, c.line)
				}
				filtered[m.name]++
			}
		}
	}
	// Pin the counts so a change that stops emitting these calls, or stops
	// rendering them on one line with their filter, is noticed here.
	if got := filtered["kitchen_sink_schema.fga"]; got != 37 {
		t.Errorf("kitchen sink has %d type-filtered direct calls, want 37", got)
	}
	if got := filtered["saas_schema.fga"]; got != 242 {
		t.Errorf("SaaS has %d type-filtered direct calls, want 242", got)
	}
}
