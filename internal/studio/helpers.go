package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
)

func resultClass(r *bool) string {
	switch {
	case r == nil:
		return ""
	case *r:
		return "ok"
	default:
		return "no"
	}
}

func short(checksum string) string {
	if len(checksum) > 12 {
		return checksum[:12]
	}
	return checksum
}

// prefill turns a type picked from the model list into an object-input seed.
func prefill(typ string) string {
	if typ == "" {
		return ""
	}
	return typ + ":"
}

// showNodeName reports whether an Expand node's userset name is worth a row
// label: not when it repeats its parent's (OpenFGA names every operand of a
// rewrite after the relation being expanded), nor when the node is just a
// pointer to that same userset.
func showNodeName(n *melange.UsersetTreeNode, parent string) bool {
	if n.Name == parent {
		return false
	}
	return n.Leaf == nil || n.Leaf.Computed == nil || n.Leaf.Computed.Userset != n.Name
}

// relationNames lists the model's distinct relation names in model order.
func relationNames(m *migrator.DeployedModel) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range m.Types {
		for _, r := range t.Relations {
			if !seen[r.Name] {
				seen[r.Name] = true
				out = append(out, r.Name)
			}
		}
	}
	return out
}

func entityURL(typ, id string) templ.SafeURL {
	return templ.URL("/entity?" + url.Values{"type": {typ}, "id": {id}}.Encode())
}

// explainVals is the hx-vals payload for an inline "why?" Explain.
func explainVals(subject, relation, object string) string {
	b, _ := json.Marshal(map[string]string{"subject": subject, "relation": relation, "object": object})
	return string(b)
}

// anchor turns a heading into a fragment id.
func anchor(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, " ", "-"))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// current marks the active navigation link for both styling and assistive tech.
func current(on bool) templ.Attributes {
	if on {
		return templ.Attributes{"aria-current": "page"}
	}
	return nil
}

// assetVersion fingerprints the embedded assets so their URLs change whenever
// their content does; browsers never keep a stale stylesheet across upgrades.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(assets, path)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// asset returns the versioned URL of an embedded asset. The version is a
// path segment, not a query, so URLs relative to a stylesheet (its fonts)
// carry it too and every asset can be cached as immutable.
func asset(name string) string { return "/assets/" + assetVersion + "/" + name }

// pageTitle is the document title: the page's own title, else its section.
func pageTitle(p page) string {
	t := p.Title
	if t == "" {
		for _, n := range navItems {
			if n.ID == p.Active {
				t = n.Label
			}
		}
	}
	return t + " · melange studio"
}

// groupShow is how many entities an access group renders before "Show more".
const groupShow = 100

func headN[T any](s []T, n int) []T { return s[:min(n, len(s))] }

// countLabel shows n, marked "n+" when a list cap may have hidden more.
func countLabel(n int, capped bool) string {
	if capped {
		return fmt.Sprintf("%d+", n)
	}
	return fmt.Sprint(n)
}

func allowed(t *melange.Trace) bool { return t.Result != nil && *t.Result }

// whatIfChange names what the what-if tuples did to the answer.
func whatIfChange(base, with *melange.Trace) string {
	switch b, w := allowed(base), allowed(with); {
	case b == w:
		return "no change"
	case w:
		return "the what-if tuples grant it"
	default:
		return "the what-if tuples revoke it"
	}
}

// isAdded reports whether evidence e is one of the what-if tuples.
func isAdded(e melange.TupleRef, added []melange.ContextualTuple) bool {
	for _, t := range added {
		if e.SubjectType == string(t.Subject.Type) && e.SubjectID == t.Subject.ID && e.Relation == string(t.Relation) &&
			e.ObjectType == string(t.Object.Type) && e.ObjectID == t.Object.ID {
			return true
		}
	}
	return false
}

// matrixCounts tallies a check matrix: allowed relations and failed checks.
func matrixCounts(cells []matrixCell) (allowed, errs int) {
	for _, c := range cells {
		if c.Err != "" {
			errs++
		} else if c.Allowed {
			allowed++
		}
	}
	return allowed, errs
}

func relationURL(typ, rel string) templ.SafeURL {
	return templ.URL("/relation?" + url.Values{"type": {typ}, "relation": {rel}}.Encode())
}

// ms formats a wall-clock duration the way the plan reports times.
func ms(d time.Duration) string { return msf(float64(d.Microseconds()) / 1000) }

// msf formats milliseconds; sub-millisecond values keep two decimals so a
// fast check does not read "0.0 ms".
func msf(v float64) string {
	if v < 0.995 { // rounds to 1.00 otherwise; give that "1.0 ms" like its neighbors
		return fmt.Sprintf("%.2f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}
