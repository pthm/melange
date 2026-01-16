/**
 * Caching for Melange authorization checks.
 *
 * This module provides cache interfaces and implementations for storing
 * permission check results to reduce database load.
 */

import type { Decision } from './types.js';

/**
 * Cache stores permission check results.
 *
 * Implementations should be safe for concurrent access if the Checker
 * is shared across requests. For request-scoped caching, create a new
 * Checker per request with a request-scoped cache.
 */
export interface Cache {
  /**
   * Get a cached decision.
   *
   * @param key - Cache key
   * @returns Cached decision, or undefined if not found or expired
   */
  get(key: string): Promise<Decision | undefined>;

  /**
   * Store a decision in the cache.
   *
   * @param key - Cache key
   * @param value - Decision to cache
   */
  set(key: string, value: Decision): Promise<void>;

  /**
   * Clear all cached entries.
   */
  clear(): Promise<void>;
}

/**
 * NoopCache is a no-op cache that never stores anything.
 *
 * This is the default cache implementation, suitable for applications
 * that don't want caching overhead or have other caching strategies.
 */
export class NoopCache implements Cache {
  async get(_key: string): Promise<Decision | undefined> {
    return undefined;
  }

  async set(_key: string, _value: Decision): Promise<void> {
    // no-op
  }

  async clear(): Promise<void> {
    // no-op
  }
}

/**
 * MemoryCache is a simple in-memory cache with TTL support.
 *
 * This cache stores decisions in a Map with time-based expiration.
 * It's suitable for single-instance applications or request-scoped caching.
 *
 * For multi-instance deployments, consider using a distributed cache
 * like Redis with a custom Cache implementation.
 *
 * @example
 * ```typescript
 * import { Checker, MemoryCache } from '@pthm/melange';
 * import { Pool } from 'pg';
 *
 * const pool = new Pool({ connectionString: process.env.DATABASE_URL });
 * const cache = new MemoryCache(60000); // 60 second TTL
 * const checker = new Checker(pool, { cache });
 *
 * // First check hits database
 * await checker.check(user, 'can_read', repo);
 *
 * // Second check within 60s uses cache
 * await checker.check(user, 'can_read', repo); // cached
 * ```
 */
export class MemoryCache implements Cache {
  private readonly cache = new Map<string, CacheEntry>();
  private readonly ttlMs: number;

  /**
   * Create a new MemoryCache.
   *
   * @param ttlMs - Time to live in milliseconds (default: 60000 = 1 minute)
   */
  constructor(ttlMs: number = 60000) {
    if (ttlMs <= 0) {
      throw new Error('TTL must be positive');
    }
    this.ttlMs = ttlMs;
  }

  async get(key: string): Promise<Decision | undefined> {
    const entry = this.cache.get(key);
    if (!entry) {
      return undefined;
    }

    // Check expiration
    if (Date.now() > entry.expiresAt) {
      this.cache.delete(key);
      return undefined;
    }

    return entry.value;
  }

  async set(key: string, value: Decision): Promise<void> {
    this.cache.set(key, {
      value,
      expiresAt: Date.now() + this.ttlMs,
    });
  }

  async clear(): Promise<void> {
    this.cache.clear();
  }

  /**
   * Get the number of entries in the cache.
   *
   * Note: This includes expired entries that haven't been accessed yet.
   */
  get size(): number {
    return this.cache.size;
  }
}

interface CacheEntry {
  value: Decision;
  expiresAt: number;
}
