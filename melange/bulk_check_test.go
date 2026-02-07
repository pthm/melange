package melange_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/pthm/melange/melange"
)

// bulkResults creates a checker with the given decision, adds n checks with
// distinct objects, executes, and returns the results.
func bulkResults(t *testing.T, decision melange.Decision, n int) *melange.BulkCheckResults {
	t.Helper()
	checker := melange.NewChecker(nil, melange.WithDecision(decision))
	b := checker.NewBulkCheck(context.Background())
	for i := range n {
		b.Add(
			melange.Object{Type: "user", ID: "1"},
			melange.Relation("can_read"),
			melange.Object{Type: "repository", ID: strconv.Itoa(i)},
		)
	}
	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return res
}

func TestBulkCheck_Add_AutoIDs(t *testing.T) {
	res := bulkResults(t, melange.DecisionAllow, 3)
	if res.Len() != 3 {
		t.Fatalf("expected 3 results, got %d", res.Len())
	}
	for i := range 3 {
		r := res.Get(i)
		if r.ID() != strconv.Itoa(i) {
			t.Errorf("result %d: expected ID %q, got %q", i, strconv.Itoa(i), r.ID())
		}
	}
}

func TestBulkCheck_AddWithID(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())
	b.AddWithID("alpha",
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "10"},
	)
	b.AddWithID("beta",
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "20"},
	)
	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	alpha := res.GetByID("alpha")
	if alpha == nil {
		t.Fatal("GetByID(alpha) returned nil")
	}
	if alpha.Object().ID != "10" {
		t.Errorf("expected object ID 10, got %s", alpha.Object().ID)
	}

	beta := res.GetByID("beta")
	if beta == nil {
		t.Fatal("GetByID(beta) returned nil")
	}
	if beta.Object().ID != "20" {
		t.Errorf("expected object ID 20, got %s", beta.Object().ID)
	}
}

func TestBulkCheck_AddWithID_PanicsOnEmptyID(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for empty ID")
		}
		msg, ok := r.(string)
		if !ok || msg != "melange: BulkCheckBuilder.AddWithID: id must not be empty" {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()

	b.AddWithID("",
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "1"},
	)
}

func TestBulkCheck_AddWithID_PanicsOnDuplicateID(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())
	b.AddWithID("dup",
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "1"},
	)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate ID")
		}
		msg, ok := r.(string)
		if !ok {
			t.Errorf("unexpected panic type: %T", r)
		}
		if msg != `melange: BulkCheckBuilder.AddWithID: duplicate id "dup"` {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()

	b.AddWithID("dup",
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "2"},
	)
}

func TestBulkCheck_AddMany(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())

	objects := []melange.ObjectLike{
		melange.Object{Type: "repository", ID: "1"},
		melange.Object{Type: "repository", ID: "2"},
		melange.Object{Type: "repository", ID: "3"},
	}
	b.AddMany(
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		objects...,
	)

	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Len() != 3 {
		t.Fatalf("expected 3 results, got %d", res.Len())
	}

	for i := range 3 {
		r := res.Get(i)
		if r.ID() != strconv.Itoa(i) {
			t.Errorf("result %d: expected ID %q, got %q", i, strconv.Itoa(i), r.ID())
		}
		if r.Object().ID != strconv.Itoa(i+1) {
			t.Errorf("result %d: expected object ID %q, got %q", i, strconv.Itoa(i+1), r.Object().ID)
		}
	}
}

func TestBulkCheck_Chaining(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())

	subject := melange.Object{Type: "user", ID: "1"}
	rel := melange.Relation("can_read")
	obj := melange.Object{Type: "repository", ID: "1"}

	result := b.
		Add(subject, rel, obj).
		AddWithID("custom", subject, rel, melange.Object{Type: "repository", ID: "2"}).
		AddMany(subject, rel, melange.Object{Type: "repository", ID: "3"})

	if result != b {
		t.Error("chained methods should return the same builder")
	}

	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Len() != 3 {
		t.Errorf("expected 3 results, got %d", res.Len())
	}
}

func TestBulkCheck_DecisionAllow(t *testing.T) {
	res := bulkResults(t, melange.DecisionAllow, 3)

	if !res.All() {
		t.Error("All() should return true when all checks are allowed")
	}
	if res.None() {
		t.Error("None() should return false when all checks are allowed")
	}
	if !res.Any() {
		t.Error("Any() should return true when all checks are allowed")
	}

	for i := range res.Len() {
		r := res.Get(i)
		if !r.IsAllowed() {
			t.Errorf("result %d should be allowed", i)
		}
	}

	if len(res.Allowed()) != 3 {
		t.Errorf("expected 3 allowed, got %d", len(res.Allowed()))
	}
	if len(res.Denied()) != 0 {
		t.Errorf("expected 0 denied, got %d", len(res.Denied()))
	}
}

func TestBulkCheck_DecisionDeny(t *testing.T) {
	res := bulkResults(t, melange.DecisionDeny, 3)

	if res.All() {
		t.Error("All() should return false when all checks are denied")
	}
	if !res.None() {
		t.Error("None() should return true when all checks are denied")
	}
	if res.Any() {
		t.Error("Any() should return false when all checks are denied")
	}

	for i := range res.Len() {
		r := res.Get(i)
		if r.IsAllowed() {
			t.Errorf("result %d should be denied", i)
		}
	}

	if len(res.Allowed()) != 0 {
		t.Errorf("expected 0 allowed, got %d", len(res.Allowed()))
	}
	if len(res.Denied()) != 3 {
		t.Errorf("expected 3 denied, got %d", len(res.Denied()))
	}
}

func TestBulkCheck_ContextDecision(t *testing.T) {
	checker := melange.NewChecker(nil,
		melange.WithDecision(melange.DecisionDeny),
		melange.WithContextDecision(),
	)
	ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)

	b := checker.NewBulkCheck(ctx)
	b.Add(
		melange.Object{Type: "user", ID: "1"},
		melange.Relation("can_read"),
		melange.Object{Type: "repository", ID: "1"},
	)

	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.All() {
		t.Error("context DecisionAllow should override checker-level DecisionDeny")
	}
}

func TestBulkCheck_EmptyBatch(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())

	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Len() != 0 {
		t.Errorf("expected 0 results, got %d", res.Len())
	}
	if res.All() {
		t.Error("All() should return false for empty batch")
	}
	if !res.None() {
		t.Error("None() should return true for empty batch")
	}
	if res.Any() {
		t.Error("Any() should return false for empty batch")
	}
	if err := res.AllOrError(); err != nil {
		t.Errorf("AllOrError() should return nil for empty batch, got %v", err)
	}
}

func TestBulkCheck_ResultAccessors(t *testing.T) {
	checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
	b := checker.NewBulkCheck(context.Background())
	b.AddWithID("check-1",
		melange.Object{Type: "user", ID: "42"},
		melange.Relation("can_write"),
		melange.Object{Type: "repository", ID: "99"},
	)

	res, err := b.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := res.Get(0)
	if r.ID() != "check-1" {
		t.Errorf("ID(): expected %q, got %q", "check-1", r.ID())
	}
	if r.Index() != 0 {
		t.Errorf("Index(): expected 0, got %d", r.Index())
	}
	if r.Subject().Type != "user" || r.Subject().ID != "42" {
		t.Errorf("Subject(): expected user:42, got %s", r.Subject())
	}
	if string(r.Relation()) != "can_write" {
		t.Errorf("Relation(): expected can_write, got %s", r.Relation())
	}
	if r.Object().Type != "repository" || r.Object().ID != "99" {
		t.Errorf("Object(): expected repository:99, got %s", r.Object())
	}
	if !r.IsAllowed() {
		t.Error("IsAllowed(): expected true")
	}
	if r.Err() != nil {
		t.Errorf("Err(): expected nil, got %v", r.Err())
	}
}

func TestBulkCheck_GetByID_NotFound(t *testing.T) {
	res := bulkResults(t, melange.DecisionAllow, 1)
	if res.GetByID("nonexistent") != nil {
		t.Error("GetByID should return nil for unknown ID")
	}
}

func TestBulkCheck_Get_PanicsOutOfRange(t *testing.T) {
	res := bulkResults(t, melange.DecisionAllow, 1)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for out of range index")
		}
	}()

	res.Get(5)
}

func TestBulkCheck_AllOrError_AllAllowed(t *testing.T) {
	res := bulkResults(t, melange.DecisionAllow, 3)
	if err := res.AllOrError(); err != nil {
		t.Errorf("AllOrError() should return nil when all allowed, got %v", err)
	}
}

func TestBulkCheck_AllOrError_Denied(t *testing.T) {
	res := bulkResults(t, melange.DecisionDeny, 3)
	err := res.AllOrError()
	if err == nil {
		t.Fatal("AllOrError() should return error when checks denied")
	}

	if !errors.Is(err, melange.ErrBulkCheckDenied) {
		t.Error("errors.Is(err, ErrBulkCheckDenied) should be true")
	}

	if !melange.IsBulkCheckDeniedErr(err) {
		t.Error("IsBulkCheckDeniedErr should return true")
	}

	var denied *melange.BulkCheckDeniedError
	if !errors.As(err, &denied) {
		t.Fatal("errors.As should succeed for *BulkCheckDeniedError")
	}
	if denied.Total != 3 {
		t.Errorf("expected Total=3, got %d", denied.Total)
	}
	if denied.Index != 0 {
		t.Errorf("expected Index=0, got %d", denied.Index)
	}
}

func TestBulkCheck_MaxBulkCheckSize(t *testing.T) {
	if melange.MaxBulkCheckSize != 10000 {
		t.Errorf("expected MaxBulkCheckSize=10000, got %d", melange.MaxBulkCheckSize)
	}
}
