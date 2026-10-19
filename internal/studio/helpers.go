package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"

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

// asset returns the versioned URL of an embedded asset.
func asset(name string) string { return "/assets/" + name + "?v=" + assetVersion }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
