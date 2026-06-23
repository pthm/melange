package render

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
)

func TestExpand_NilAndEmpty(t *testing.T) {
	if got := ExpandString(nil); got != "" {
		t.Errorf("nil tree should render empty; got %q", got)
	}
	got := ExpandString(&melange.UsersetTree{})
	if !strings.Contains(got, "(empty tree)") {
		t.Errorf("empty-tree placeholder missing; got %q", got)
	}
}

func TestExpand_LeafUsersInline(t *testing.T) {
	tree := &melange.UsersetTree{
		Root: &melange.UsersetTreeNode{
			Name: "document:1#viewer",
			Leaf: &melange.Leaf{
				Users: &melange.Users{
					Users: []string{"user:alice", "user:bob", "group:eng#member"},
				},
			},
		},
	}
	got := ExpandString(tree)
	// Header carries name + leaf-kind discriminator
	if !strings.Contains(got, "document:1#viewer • users") {
		t.Errorf("header missing; got:\n%s", got)
	}
	// Each user prints on its own line
	for _, u := range []string{"user:alice", "user:bob", "group:eng#member"} {
		if !strings.Contains(got, u) {
			t.Errorf("user %q missing; got:\n%s", u, got)
		}
	}
}

func TestExpand_LeafUsersTruncated(t *testing.T) {
	tree := &melange.UsersetTree{
		Root: &melange.UsersetTreeNode{
			Name: "document:1#viewer",
			Leaf: &melange.Leaf{
				Users: &melange.Users{
					Users:          []string{"user:alice"},
					UsersTruncated: true,
				},
			},
		},
	}
	got := ExpandString(tree)
	if !strings.Contains(got, "users_truncated") {
		t.Errorf("truncation warning missing; got:\n%s", got)
	}
	if !strings.Contains(got, "raise --max-leaf") {
		t.Errorf("truncation hint missing; got:\n%s", got)
	}
}

func TestExpand_LeafComputedPointer(t *testing.T) {
	tree := &melange.UsersetTree{
		Root: &melange.UsersetTreeNode{
			Name: "document:1#viewer",
			Leaf: &melange.Leaf{
				Computed: &melange.Computed{Userset: "document:1#editor"},
			},
		},
	}
	got := ExpandString(tree)
	if !strings.Contains(got, "• computed pointer") {
		t.Errorf("computed discriminator missing; got:\n%s", got)
	}
	if !strings.Contains(got, "computed → document:1#editor") {
		t.Errorf("computed pointer body missing; got:\n%s", got)
	}
	// Caller hint so users know they can chase the pointer
	if !strings.Contains(got, "melange expand document:1#editor") {
		t.Errorf("chase hint missing; got:\n%s", got)
	}
}

func TestExpand_UnionOfTwo(t *testing.T) {
	tree := &melange.UsersetTree{
		Root: &melange.UsersetTreeNode{
			Name: "document:1#viewer",
			Union: &melange.Nodes{
				Nodes: []*melange.UsersetTreeNode{
					{
						Name: "document:1#viewer",
						Leaf: &melange.Leaf{
							Users: &melange.Users{Users: []string{"user:alice"}},
						},
					},
					{
						Name: "document:1#viewer",
						Leaf: &melange.Leaf{
							Computed: &melange.Computed{Userset: "document:1#editor"},
						},
					},
				},
			},
		},
	}
	got := ExpandString(tree)
	if !strings.Contains(got, "• union of 2") {
		t.Errorf("union summary missing; got:\n%s", got)
	}
	if !strings.Contains(got, "user:alice") {
		t.Errorf("first child users missing; got:\n%s", got)
	}
	if !strings.Contains(got, "computed → document:1#editor") {
		t.Errorf("second child pointer missing; got:\n%s", got)
	}
}

func TestExpand_DifferenceNamedSlots(t *testing.T) {
	// Reserved for slice 2.2 but the renderer must already handle the
	// shape since Difference has named (not positional) children — the
	// rendering decision is the same regardless of which slice ships it.
	tree := &melange.UsersetTree{
		Root: &melange.UsersetTreeNode{
			Name: "document:1#can_review",
			Difference: &melange.Difference{
				Base: &melange.UsersetTreeNode{
					Name: "document:1#can_read",
					Leaf: &melange.Leaf{Users: &melange.Users{Users: []string{"user:alice"}}},
				},
				Subtract: &melange.UsersetTreeNode{
					Name: "document:1#author",
					Leaf: &melange.Leaf{Users: &melange.Users{Users: []string{"user:bob"}}},
				},
			},
		},
	}
	got := ExpandString(tree)
	if !strings.Contains(got, "difference (base / subtract)") {
		t.Errorf("difference label missing; got:\n%s", got)
	}
	if !strings.Contains(got, "document:1#can_read") || !strings.Contains(got, "document:1#author") {
		t.Errorf("named slots missing; got:\n%s", got)
	}
}
