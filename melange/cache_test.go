package melange

import (
	"errors"
	"testing"
	"time"
)

// TestCache_ExplainKeyIncludesMaxNodes pins the promise that different
// p_max_nodes caps produce different cache entries — a truncated trace
// for one cap must not be served to a caller that passed a larger cap.
func TestCache_ExplainKeyIncludesMaxNodes(t *testing.T) {
	c := NewCache()
	subj := Object{Type: "user", ID: "alice"}
	obj := Object{Type: "document", ID: "1"}
	rel := Relation("viewer")

	trace50 := &Trace{}
	trace200 := &Trace{}

	c.SetExplain(subj, rel, obj, 50, trace50, nil)
	c.SetExplain(subj, rel, obj, 200, trace200, nil)

	got50, _, ok50 := c.GetExplain(subj, rel, obj, 50)
	if !ok50 || got50 != trace50 {
		t.Errorf("GetExplain(50) = (%v, _, %v), want (trace50, _, true)", got50, ok50)
	}
	got200, _, ok200 := c.GetExplain(subj, rel, obj, 200)
	if !ok200 || got200 != trace200 {
		t.Errorf("GetExplain(200) = (%v, _, %v), want (trace200, _, true)", got200, ok200)
	}
	_, _, ok100 := c.GetExplain(subj, rel, obj, 100)
	if ok100 {
		t.Errorf("GetExplain(100) should miss — no entry stored at that cap")
	}
}

// TestCache_ExpandKeyIncludesSubjectTypeAndMaxLeaf pins the same
// promise for Expand: filter and cap both change the tree, so they
// participate in the key.
func TestCache_ExpandKeyIncludesSubjectTypeAndMaxLeaf(t *testing.T) {
	c := NewCache()
	obj := Object{Type: "document", ID: "1"}
	rel := Relation("viewer")

	treeAll := &UsersetTree{}
	treeUser := &UsersetTree{}
	treeCapped := &UsersetTree{}

	c.SetExpand(obj, rel, "", 0, treeAll, nil)
	c.SetExpand(obj, rel, "user", 0, treeUser, nil)
	c.SetExpand(obj, rel, "", 100, treeCapped, nil)

	gotAll, _, okAll := c.GetExpand(obj, rel, "", 0)
	if !okAll || gotAll != treeAll {
		t.Errorf("GetExpand(unfiltered, uncapped) miss")
	}
	gotUser, _, okUser := c.GetExpand(obj, rel, "user", 0)
	if !okUser || gotUser != treeUser {
		t.Errorf("GetExpand(user, uncapped) miss")
	}
	gotCapped, _, okCapped := c.GetExpand(obj, rel, "", 100)
	if !okCapped || gotCapped != treeCapped {
		t.Errorf("GetExpand(unfiltered, capped) miss")
	}
	// Cross-check: subject-type filter doesn't collide with cap.
	_, _, okCross := c.GetExpand(obj, rel, "group", 0)
	if okCross {
		t.Errorf("GetExpand with unset filter should miss for 'group'")
	}
}

// TestCache_ClearNukesAllThreeFamilies confirms Clear resets Check,
// Explain, and Expand entries — one blanket reset semantics.
func TestCache_ClearNukesAllThreeFamilies(t *testing.T) {
	c := NewCache()
	subj := Object{Type: "user", ID: "alice"}
	obj := Object{Type: "document", ID: "1"}
	rel := Relation("viewer")

	c.Set(subj, rel, obj, true, nil)
	c.SetExplain(subj, rel, obj, 0, &Trace{}, nil)
	c.SetExpand(obj, rel, "", 0, &UsersetTree{}, nil)

	if c.Size() != 3 {
		t.Errorf("Size after 3 sets = %d, want 3", c.Size())
	}

	c.Clear()

	if c.Size() != 0 {
		t.Errorf("Size after Clear = %d, want 0", c.Size())
	}
	if _, _, ok := c.Get(subj, rel, obj); ok {
		t.Errorf("Check entry survived Clear")
	}
	if _, _, ok := c.GetExplain(subj, rel, obj, 0); ok {
		t.Errorf("Explain entry survived Clear")
	}
	if _, _, ok := c.GetExpand(obj, rel, "", 0); ok {
		t.Errorf("Expand entry survived Clear")
	}
}

// TestCache_TTLExpiresAllFamilies confirms the shared TTL applies to
// every family (WithTTL is a Cache-wide setting).
func TestCache_TTLExpiresAllFamilies(t *testing.T) {
	c := NewCache(WithTTL(10 * time.Millisecond))
	subj := Object{Type: "user", ID: "alice"}
	obj := Object{Type: "document", ID: "1"}
	rel := Relation("viewer")

	c.Set(subj, rel, obj, true, nil)
	c.SetExplain(subj, rel, obj, 0, &Trace{}, nil)
	c.SetExpand(obj, rel, "", 0, &UsersetTree{}, nil)

	time.Sleep(15 * time.Millisecond)

	if _, _, ok := c.Get(subj, rel, obj); ok {
		t.Errorf("Check entry survived TTL")
	}
	if _, _, ok := c.GetExplain(subj, rel, obj, 0); ok {
		t.Errorf("Explain entry survived TTL")
	}
	if _, _, ok := c.GetExpand(obj, rel, "", 0); ok {
		t.Errorf("Expand entry survived TTL")
	}
}

// TestCache_ExplainStoresErrors documents that errors are cacheable
// via the interface (matches Check's Cache.Set signature) but the
// Checker chooses not to cache them (err == nil gate at the call
// site). This test is the interface-level contract.
func TestCache_ExplainStoresErrors(t *testing.T) {
	c := NewCache()
	subj := Object{Type: "user", ID: "alice"}
	obj := Object{Type: "document", ID: "1"}
	rel := Relation("viewer")

	sentinel := errors.New("boom")
	c.SetExplain(subj, rel, obj, 0, nil, sentinel)

	_, cachedErr, ok := c.GetExplain(subj, rel, obj, 0)
	if !ok {
		t.Fatalf("GetExplain miss after Set")
	}
	if !errors.Is(cachedErr, sentinel) {
		t.Errorf("cached err = %v, want %v", cachedErr, sentinel)
	}
}

// TestCache_ImplementsAllInterfaces guards against a future
// refactor accidentally dropping one of the opt-in interfaces
// from CacheImpl.
func TestCache_ImplementsAllInterfaces(t *testing.T) {
	c := NewCache()
	if _, ok := any(c).(Cache); !ok {
		t.Errorf("CacheImpl does not implement Cache")
	}
	if _, ok := any(c).(ExplainCache); !ok {
		t.Errorf("CacheImpl does not implement ExplainCache")
	}
	if _, ok := any(c).(ExpandCache); !ok {
		t.Errorf("CacheImpl does not implement ExpandCache")
	}
}
