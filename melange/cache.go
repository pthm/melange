package melange

import (
	"sync"
	"time"
)

// cacheKey uniquely identifies a permission check.
// All fields are required to form a unique key. Partial matches are not
// supported - the cache is exact-match only.
type cacheKey struct {
	SubjectType ObjectType
	SubjectID   string
	Relation    Relation
	ObjectType  ObjectType
	ObjectID    string
}

// explainCacheKey uniquely identifies an Explain call. Same shape as
// cacheKey plus the per-call MaxNodes cap — different p_max_nodes
// produces different traces, so they must be distinct entries.
type explainCacheKey struct {
	SubjectType ObjectType
	SubjectID   string
	Relation    Relation
	ObjectType  ObjectType
	ObjectID    string
	MaxNodes    int
}

// expandCacheKey uniquely identifies an Expand call. Includes the
// subject-type filter and per-leaf cap because both affect the emitted
// UsersetTree shape.
type expandCacheKey struct {
	ObjectType  ObjectType
	ObjectID    string
	Relation    Relation
	SubjectType ObjectType // "" == unset (no filter)
	MaxLeaf     int
}

// cacheEntry stores the result of a permission check.
// Both successful and failed checks are cached, including errors.
// This prevents repeated queries for denied permissions.
type cacheEntry struct {
	allowed   bool
	err       error
	expiresAt time.Time // zero means no expiry
}

// explainCacheEntry stores the result of an Explain call.
type explainCacheEntry struct {
	trace     *Trace
	err       error
	expiresAt time.Time
}

// expandCacheEntry stores the result of an Expand call.
type expandCacheEntry struct {
	tree      *UsersetTree
	err       error
	expiresAt time.Time
}

// Cache stores permission check results.
// It is safe for concurrent use from multiple goroutines.
//
// Implementations should cache both allowed and denied permissions, including
// errors. This reduces database load for repeated checks of denied access.
type Cache interface {
	// Get retrieves a cached permission check result.
	// Returns (allowed, err, found). If found is false, the entry doesn't exist or is expired.
	Get(subject Object, relation Relation, object Object) (allowed bool, err error, ok bool)

	// Set stores a permission check result in the cache.
	Set(subject Object, relation Relation, object Object, allowed bool, err error)
}

// ExplainCache is the opt-in extension for caching Explain traces.
// Implement it on a Cache to have Checker.Explain consult and populate
// the cache. Injected caches that don't implement it fall through to
// the DB on every Explain call — Check-only caches remain compatible.
//
// The per-call MaxNodes cap is part of the key: different caps produce
// different traces (truncation flips) so they can't share entries.
type ExplainCache interface {
	// GetExplain retrieves a cached Explain trace. Returns (trace, err,
	// found). found=false means "not present or expired"; the caller
	// should fall through to the DB.
	GetExplain(subject Object, relation Relation, object Object, maxNodes int) (trace *Trace, err error, ok bool)

	// SetExplain stores an Explain trace. Callers only Set on
	// successful calls (err == nil) — matching the Check cache
	// convention that failed calls aren't cached.
	SetExplain(subject Object, relation Relation, object Object, maxNodes int, trace *Trace, err error)
}

// ExpandCache is the opt-in extension for caching Expand trees.
// Implement it on a Cache to have Checker.Expand consult and populate
// the cache. See ExplainCache for the same contract; SubjectType and
// MaxLeaf are part of the key because both affect the tree shape.
type ExpandCache interface {
	GetExpand(object Object, relation Relation, subjectType ObjectType, maxLeaf int) (tree *UsersetTree, err error, ok bool)
	SetExpand(object Object, relation Relation, subjectType ObjectType, maxLeaf int, tree *UsersetTree, err error)
}

// CacheImpl is the default in-memory cache implementation with optional TTL.
// It uses a sync.RWMutex for goroutine safety. For high-contention scenarios,
// consider a sharded cache or external cache (Redis, etc.).
//
// The cache grows unbounded within its TTL window. For long-running processes
// with large permission sets, consider periodic clearing or TTL-based expiry.
//
// A single mutex covers all three families (Check / Explain / Expand);
// each has its own map so the key structs stay strongly typed. Clear()
// nukes all three at once — the same tuple write that would invalidate
// a cached Check would also invalidate cached Explain / Expand results,
// so one blanket Clear is the right coarse-grained reset.
type CacheImpl struct {
	mu       sync.RWMutex
	items    map[cacheKey]cacheEntry
	explains map[explainCacheKey]explainCacheEntry
	expands  map[expandCacheKey]expandCacheEntry
	ttl      time.Duration // 0 means no expiry
}

// CacheOption configures a Cache.
type CacheOption func(*CacheImpl)

// WithTTL sets the time-to-live for cache entries.
// Entries older than TTL are considered stale and will be re-checked.
// A TTL of 0 (default) means entries never expire within the cache's lifetime.
//
// Choose TTL based on permission volatility:
//   - Short TTL (seconds): Frequently changing permissions, high security
//   - Medium TTL (minutes): Typical web applications
//   - Long TTL or none: Near-static permissions, performance-critical paths
func WithTTL(ttl time.Duration) CacheOption {
	return func(c *CacheImpl) {
		c.ttl = ttl
	}
}

// NewCache creates a new permission cache.
// The cache is safe for concurrent use but scoped to a single process.
// For distributed systems, implement Cache with a distributed store.
func NewCache(opts ...CacheOption) *CacheImpl {
	c := &CacheImpl{
		items:    make(map[cacheKey]cacheEntry),
		explains: make(map[explainCacheKey]explainCacheEntry),
		expands:  make(map[expandCacheKey]expandCacheEntry),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get retrieves a cached permission check result.
// Returns (allowed, err, found). If found is false, the entry doesn't exist or is expired.
func (c *CacheImpl) Get(subject Object, relation Relation, object Object) (bool, error, bool) {
	key := cacheKey{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		Relation:    relation,
		ObjectType:  object.Type,
		ObjectID:    object.ID,
	}

	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return false, nil, false
	}

	// Check expiry if TTL is set
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		// Entry expired, remove it
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return false, nil, false
	}

	return entry.allowed, entry.err, true
}

// Set stores a permission check result in the cache.
func (c *CacheImpl) Set(subject Object, relation Relation, object Object, allowed bool, err error) {
	key := cacheKey{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		Relation:    relation,
		ObjectType:  object.Type,
		ObjectID:    object.ID,
	}

	entry := cacheEntry{
		allowed: allowed,
		err:     err,
	}

	if c.ttl > 0 {
		entry.expiresAt = time.Now().Add(c.ttl)
	}

	c.mu.Lock()
	c.items[key] = entry
	c.mu.Unlock()
}

// GetExplain retrieves a cached Explain trace.
func (c *CacheImpl) GetExplain(subject Object, relation Relation, object Object, maxNodes int) (*Trace, error, bool) {
	key := explainCacheKey{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		Relation:    relation,
		ObjectType:  object.Type,
		ObjectID:    object.ID,
		MaxNodes:    maxNodes,
	}

	c.mu.RLock()
	entry, ok := c.explains[key]
	c.mu.RUnlock()

	if !ok {
		return nil, nil, false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.explains, key)
		c.mu.Unlock()
		return nil, nil, false
	}
	return entry.trace, entry.err, true
}

// SetExplain stores an Explain trace.
func (c *CacheImpl) SetExplain(subject Object, relation Relation, object Object, maxNodes int, trace *Trace, err error) {
	key := explainCacheKey{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		Relation:    relation,
		ObjectType:  object.Type,
		ObjectID:    object.ID,
		MaxNodes:    maxNodes,
	}
	entry := explainCacheEntry{trace: trace, err: err}
	if c.ttl > 0 {
		entry.expiresAt = time.Now().Add(c.ttl)
	}
	c.mu.Lock()
	c.explains[key] = entry
	c.mu.Unlock()
}

// GetExpand retrieves a cached Expand tree.
func (c *CacheImpl) GetExpand(object Object, relation Relation, subjectType ObjectType, maxLeaf int) (*UsersetTree, error, bool) {
	key := expandCacheKey{
		ObjectType:  object.Type,
		ObjectID:    object.ID,
		Relation:    relation,
		SubjectType: subjectType,
		MaxLeaf:     maxLeaf,
	}

	c.mu.RLock()
	entry, ok := c.expands[key]
	c.mu.RUnlock()

	if !ok {
		return nil, nil, false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.expands, key)
		c.mu.Unlock()
		return nil, nil, false
	}
	return entry.tree, entry.err, true
}

// SetExpand stores an Expand tree.
func (c *CacheImpl) SetExpand(object Object, relation Relation, subjectType ObjectType, maxLeaf int, tree *UsersetTree, err error) {
	key := expandCacheKey{
		ObjectType:  object.Type,
		ObjectID:    object.ID,
		Relation:    relation,
		SubjectType: subjectType,
		MaxLeaf:     maxLeaf,
	}
	entry := expandCacheEntry{tree: tree, err: err}
	if c.ttl > 0 {
		entry.expiresAt = time.Now().Add(c.ttl)
	}
	c.mu.Lock()
	c.expands[key] = entry
	c.mu.Unlock()
}

// Size returns the total number of entries across all three
// cache families (Check + Explain + Expand). Useful for monitoring
// cache growth and memory usage.
func (c *CacheImpl) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items) + len(c.explains) + len(c.expands)
}

// Clear removes all entries across all three cache families.
// Useful for testing or when permission data changes globally
// (e.g., after schema migration or mass permission updates).
func (c *CacheImpl) Clear() {
	c.mu.Lock()
	c.items = make(map[cacheKey]cacheEntry)
	c.explains = make(map[explainCacheKey]explainCacheEntry)
	c.expands = make(map[expandCacheKey]expandCacheEntry)
	c.mu.Unlock()
}

// Ensure CacheImpl implements Cache, ExplainCache, and ExpandCache.
var (
	_ Cache        = (*CacheImpl)(nil)
	_ ExplainCache = (*CacheImpl)(nil)
	_ ExpandCache  = (*CacheImpl)(nil)
)
