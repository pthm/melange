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

// cacheEntry stores the result of a permission check.
// Both successful and failed checks are cached, including errors.
// This prevents repeated queries for denied permissions.
type cacheEntry struct {
	allowed   bool
	err       error
	expiresAt time.Time // zero means no expiry
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

// CacheImpl is the default in-memory cache implementation with optional TTL.
// It uses a sync.RWMutex for goroutine safety. For high-contention scenarios,
// consider a sharded cache or external cache (Redis, etc.).
//
// The cache grows unbounded within its TTL window. For long-running processes
// with large permission sets, consider periodic clearing or TTL-based expiry.
type CacheImpl struct {
	mu    sync.RWMutex
	items map[cacheKey]cacheEntry
	ttl   time.Duration // 0 means no expiry
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
		items: make(map[cacheKey]cacheEntry),
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

// Size returns the number of entries in the cache.
// Useful for monitoring cache growth and memory usage.
func (c *CacheImpl) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Clear removes all entries from the cache.
// Useful for testing or when permission data changes globally
// (e.g., after schema migration or mass permission updates).
func (c *CacheImpl) Clear() {
	c.mu.Lock()
	c.items = make(map[cacheKey]cacheEntry)
	c.mu.Unlock()
}

// Ensure CacheImpl implements Cache.
var _ Cache = (*CacheImpl)(nil)
