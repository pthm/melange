package melange

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// MaxBulkCheckSize is the maximum number of checks allowed in a single bulk
// operation. This prevents accidental resource exhaustion from unbounded batches.
const MaxBulkCheckSize = 10000

// bulkCheckRequest holds a single check within a bulk batch.
type bulkCheckRequest struct {
	id       string
	subject  SubjectLike
	relation RelationLike
	object   ObjectLike
}

// dedupeKey identifies a unique permission check for deduplication.
type dedupeKey struct {
	subjectType, subjectID, relation, objectType, objectID string
}

// BulkCheckBuilder accumulates permission checks and executes them in a single
// SQL call via check_permission_bulk. Use Checker.NewBulkCheck to create one.
type BulkCheckBuilder struct {
	checker          *Checker
	ctx              context.Context
	requests         []bulkCheckRequest
	contextualTuples []ContextualTuple
	ids              map[string]struct{}
	databaseSchema   string
}

// Add appends a permission check to the batch with an auto-generated ID
// (the string representation of the check's index). Returns the builder
// for chaining.
func (b *BulkCheckBuilder) Add(subject SubjectLike, relation RelationLike, object ObjectLike) *BulkCheckBuilder {
	id := strconv.Itoa(len(b.requests))
	b.ids[id] = struct{}{}
	b.requests = append(b.requests, bulkCheckRequest{
		id:       id,
		subject:  subject,
		relation: relation,
		object:   object,
	})
	return b
}

// AddWithID appends a permission check with a caller-supplied ID.
// The ID must be non-empty and unique within the batch; duplicates panic.
func (b *BulkCheckBuilder) AddWithID(id string, subject SubjectLike, relation RelationLike, object ObjectLike) *BulkCheckBuilder {
	if id == "" {
		panic("melange: BulkCheckBuilder.AddWithID: id must not be empty")
	}
	if _, exists := b.ids[id]; exists {
		panic(fmt.Sprintf("melange: BulkCheckBuilder.AddWithID: duplicate id %q", id))
	}
	b.ids[id] = struct{}{}
	b.requests = append(b.requests, bulkCheckRequest{
		id:       id,
		subject:  subject,
		relation: relation,
		object:   object,
	})
	return b
}

// AddMany appends checks for one subject+relation across multiple objects.
// Each check gets an auto-generated ID.
func (b *BulkCheckBuilder) AddMany(subject SubjectLike, relation RelationLike, objects ...ObjectLike) *BulkCheckBuilder {
	for _, obj := range objects {
		b.Add(subject, relation, obj)
	}
	return b
}

// WithContextualTuples attaches contextual tuples to this bulk check.
// They are installed once before the SQL call and torn down afterwards.
func (b *BulkCheckBuilder) WithContextualTuples(tuples ...ContextualTuple) *BulkCheckBuilder {
	b.contextualTuples = append(b.contextualTuples, tuples...)
	return b
}

// Execute runs all accumulated checks in a single SQL call to
// check_permission_bulk. Results honor decision overrides, caching,
// deduplication, and contextual tuples.
func (b *BulkCheckBuilder) Execute() (*BulkCheckResults, error) {
	c := b.checker
	ctx := b.ctx

	// 1. Decision overrides — context then checker level.
	if c.useContextDecision {
		if d := GetDecisionContext(ctx); d != DecisionUnset {
			return b.buildAllDecision(d == DecisionAllow), nil
		}
	}
	if c.decision != DecisionUnset {
		return b.buildAllDecision(c.decision == DecisionAllow), nil
	}

	// 2. Empty batch → empty results.
	if len(b.requests) == 0 {
		return &BulkCheckResults{byID: make(map[string]*BulkCheckResult)}, nil
	}

	// 3. Batch size guard.
	if len(b.requests) > MaxBulkCheckSize {
		return nil, fmt.Errorf("melange: bulk check size %d exceeds maximum %d", len(b.requests), MaxBulkCheckSize)
	}

	// 4. Validation.
	for i := range b.requests {
		r := &b.requests[i]
		if c.validateUserset {
			if err := c.validateUsersetSubject(ctx, c.q, r.subject.FGASubject()); err != nil {
				return nil, fmt.Errorf("request %d: %w", i, err)
			}
		}
		if c.validateRequest {
			if err := c.validateCheckRequest(ctx, c.q, r.subject.FGASubject(), r.relation.FGARelation(), r.object.FGAObject()); err != nil {
				return nil, fmt.Errorf("request %d: %w", i, err)
			}
		}
	}
	if len(b.contextualTuples) > 0 {
		if err := c.validateContextualTuples(ctx, b.contextualTuples); err != nil {
			return nil, err
		}
	}

	// 5. Deduplication — map unique checks → original indices.
	type uniqueEntry struct {
		indices []int // positions in b.requests
	}
	dedup := make(map[dedupeKey]*uniqueEntry, len(b.requests))
	var unique []dedupeKey
	for i, r := range b.requests {
		key := dedupeKey{
			subjectType: string(r.subject.FGASubject().Type),
			subjectID:   r.subject.FGASubject().ID,
			relation:    string(r.relation.FGARelation()),
			objectType:  string(r.object.FGAObject().Type),
			objectID:    r.object.FGAObject().ID,
		}
		if entry, ok := dedup[key]; ok {
			entry.indices = append(entry.indices, i)
		} else {
			dedup[key] = &uniqueEntry{indices: []int{i}}
			unique = append(unique, key)
		}
	}

	// 6. Cache lookup.
	type checkOutcome struct {
		allowed bool
		err     error
	}
	uniqueResults := make(map[int]checkOutcome, len(unique))
	var uncachedKeys []int // indices into unique that need SQL
	for i, key := range unique {
		if c.cache != nil {
			subj := Object{Type: ObjectType(key.subjectType), ID: key.subjectID}
			rel := Relation(key.relation)
			obj := Object{Type: ObjectType(key.objectType), ID: key.objectID}
			if allowed, cachedErr, found := c.cache.Get(subj, rel, obj); found {
				uniqueResults[i] = checkOutcome{allowed: allowed, err: cachedErr}
				continue
			}
		}
		uncachedKeys = append(uncachedKeys, i)
	}

	// 7. Determine querier — contextual tuples need connection pinning.
	q := c.q
	if len(b.contextualTuples) > 0 {
		execer, cleanup, err := c.prepareContextualTuples(ctx, b.contextualTuples)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		q = execer
	}

	if len(uncachedKeys) > 0 {
		subjectTypes := make([]string, len(uncachedKeys))
		subjectIDs := make([]string, len(uncachedKeys))
		relations := make([]string, len(uncachedKeys))
		objectTypes := make([]string, len(uncachedKeys))
		objectIDs := make([]string, len(uncachedKeys))

		for i, uIdx := range uncachedKeys {
			key := unique[uIdx]
			subjectTypes[i] = key.subjectType
			subjectIDs[i] = key.subjectID
			relations[i] = key.relation
			objectTypes[i] = key.objectType
			objectIDs[i] = key.objectID
		}

		rows, err := q.QueryContext(ctx,
			fmt.Sprintf("SELECT idx, allowed FROM %s($1, $2, $3, $4, $5)", prefixIdent("check_permission_bulk", c.databaseSchema)),
			textArray(subjectTypes),
			textArray(subjectIDs),
			textArray(relations),
			textArray(objectTypes),
			textArray(objectIDs),
		)
		if err != nil {
			return nil, c.mapError("check_permission_bulk", err)
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var sqlIdx int
			var allowed int
			if err := rows.Scan(&sqlIdx, &allowed); err != nil {
				return nil, err
			}
			// SQL idx is 1-based (WITH ORDINALITY), convert to 0-based index into uncachedKeys.
			zeroIdx := sqlIdx - 1
			if zeroIdx < 0 || zeroIdx >= len(uncachedKeys) {
				continue
			}
			uIdx := uncachedKeys[zeroIdx]
			uniqueResults[uIdx] = checkOutcome{allowed: allowed == 1}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		// 9. Cache store for DB results.
		if c.cache != nil {
			for _, zeroIdx := range uncachedKeys {
				hit, ok := uniqueResults[zeroIdx]
				if !ok {
					continue
				}
				key := unique[zeroIdx]
				subj := Object{Type: ObjectType(key.subjectType), ID: key.subjectID}
				rel := Relation(key.relation)
				obj := Object{Type: ObjectType(key.objectType), ID: key.objectID}
				c.cache.Set(subj, rel, obj, hit.allowed, nil)
			}
		}
	}

	// 10. Result assembly — fan out deduplicated results to all original indices.
	results := make([]BulkCheckResult, len(b.requests))
	byID := make(map[string]*BulkCheckResult, len(b.requests))

	for i, key := range unique {
		entry := dedup[key]
		hit := uniqueResults[i]
		for _, origIdx := range entry.indices {
			r := &b.requests[origIdx]
			results[origIdx] = BulkCheckResult{
				id:       r.id,
				index:    origIdx,
				subject:  r.subject.FGASubject(),
				relation: r.relation.FGARelation(),
				object:   r.object.FGAObject(),
				allowed:  hit.allowed,
				err:      hit.err,
			}
		}
	}

	for i := range results {
		byID[results[i].id] = &results[i]
	}

	return &BulkCheckResults{results: results, byID: byID}, nil
}

// buildAllDecision creates results where every check has the same outcome.
func (b *BulkCheckBuilder) buildAllDecision(allowed bool) *BulkCheckResults {
	results := make([]BulkCheckResult, len(b.requests))
	byID := make(map[string]*BulkCheckResult, len(b.requests))
	for i, r := range b.requests {
		results[i] = BulkCheckResult{
			id:       r.id,
			index:    i,
			subject:  r.subject.FGASubject(),
			relation: r.relation.FGARelation(),
			object:   r.object.FGAObject(),
			allowed:  allowed,
		}
		byID[r.id] = &results[i]
	}
	return &BulkCheckResults{results: results, byID: byID}
}

// ---------- textArray: stdlib-compatible PostgreSQL text[] encoder ----------

// textArray encodes a Go string slice as a PostgreSQL text[] literal.
// It implements database/sql/driver.Valuer so it works with any SQL driver
// that respects Valuer (lib/pq, pgx, etc.) without importing either.
type textArray []string

// Value implements driver.Valuer.
func (a textArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, c := range s {
			if c == '"' || c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(c)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// ---------- BulkCheckResult ----------

// BulkCheckResult holds the outcome of a single check within a bulk batch.
type BulkCheckResult struct {
	id       string
	index    int
	subject  Object
	relation Relation
	object   Object
	allowed  bool
	err      error
}

// ID returns the caller-supplied or auto-generated ID for this check.
func (r *BulkCheckResult) ID() string { return r.id }

// Index returns the position of this check in the original request order.
func (r *BulkCheckResult) Index() int { return r.index }

// Subject returns the subject of this check.
func (r *BulkCheckResult) Subject() Object { return r.subject }

// Relation returns the relation of this check.
func (r *BulkCheckResult) Relation() Relation { return r.relation }

// Object returns the object of this check.
func (r *BulkCheckResult) Object() Object { return r.object }

// IsAllowed returns true if the permission was granted and no error occurred.
func (r *BulkCheckResult) IsAllowed() bool { return r.allowed && r.err == nil }

// Err returns any error that occurred during this individual check.
func (r *BulkCheckResult) Err() error { return r.err }

// ---------- BulkCheckResults ----------

// BulkCheckResults holds the outcomes of a bulk permission check.
// Results are in the same order as the original requests.
type BulkCheckResults struct {
	results []BulkCheckResult
	byID    map[string]*BulkCheckResult
}

// Len returns the number of results.
func (r *BulkCheckResults) Len() int { return len(r.results) }

// Get returns the result at the given index. Panics if out of range.
func (r *BulkCheckResults) Get(index int) *BulkCheckResult { return &r.results[index] }

// GetByID returns the result with the given ID, or nil if not found.
func (r *BulkCheckResults) GetByID(id string) *BulkCheckResult { return r.byID[id] }

// All returns true if every check was allowed with no errors.
func (r *BulkCheckResults) All() bool {
	for i := range r.results {
		if !r.results[i].IsAllowed() {
			return false
		}
	}
	return len(r.results) > 0
}

// Any returns true if at least one check was allowed with no error.
func (r *BulkCheckResults) Any() bool {
	for i := range r.results {
		if r.results[i].IsAllowed() {
			return true
		}
	}
	return false
}

// None returns true if no check was allowed (all denied or errored).
func (r *BulkCheckResults) None() bool {
	for i := range r.results {
		if r.results[i].IsAllowed() {
			return false
		}
	}
	return true
}

// Results returns pointers to all results in request order.
func (r *BulkCheckResults) Results() []*BulkCheckResult {
	out := make([]*BulkCheckResult, len(r.results))
	for i := range r.results {
		out[i] = &r.results[i]
	}
	return out
}

// Allowed returns only the results where the check was allowed.
func (r *BulkCheckResults) Allowed() []*BulkCheckResult {
	var out []*BulkCheckResult
	for i := range r.results {
		if r.results[i].IsAllowed() {
			out = append(out, &r.results[i])
		}
	}
	return out
}

// Denied returns results where the check was denied or errored.
func (r *BulkCheckResults) Denied() []*BulkCheckResult {
	var out []*BulkCheckResult
	for i := range r.results {
		if !r.results[i].IsAllowed() {
			out = append(out, &r.results[i])
		}
	}
	return out
}

// Errors collects non-nil errors from all results.
func (r *BulkCheckResults) Errors() []error {
	var out []error
	for i := range r.results {
		if r.results[i].err != nil {
			out = append(out, r.results[i].err)
		}
	}
	return out
}

// AllOrError returns nil if all checks were allowed, or a *BulkCheckDeniedError
// wrapping ErrBulkCheckDenied describing the first denied check.
func (r *BulkCheckResults) AllOrError() error {
	var first *BulkCheckResult
	deniedCount := 0
	for i := range r.results {
		if !r.results[i].IsAllowed() {
			if first == nil {
				first = &r.results[i]
			}
			deniedCount++
		}
	}
	if first == nil {
		return nil
	}
	return &BulkCheckDeniedError{
		Subject:  first.subject,
		Relation: first.relation,
		Object:   first.object,
		Index:    first.index,
		Total:    deniedCount,
	}
}
