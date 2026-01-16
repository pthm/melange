/**
 * Unit tests for Cache implementations.
 */

import { describe, test, expect, beforeEach, vi } from 'vitest';
import { NoopCache, MemoryCache } from './cache.js';
import type { Decision } from './types.js';

describe('NoopCache', () => {
  let cache: NoopCache;

  beforeEach(() => {
    cache = new NoopCache();
  });

  test('get always returns undefined', async () => {
    const result = await cache.get('any-key');
    expect(result).toBeUndefined();
  });

  test('set does nothing', async () => {
    const decision: Decision = { allowed: true };
    await expect(cache.set('key', decision)).resolves.toBeUndefined();
  });

  test('clear does nothing', async () => {
    await expect(cache.clear()).resolves.toBeUndefined();
  });
});

describe('MemoryCache', () => {
  test('stores and retrieves values', async () => {
    const cache = new MemoryCache();
    const decision: Decision = { allowed: true };

    await cache.set('test-key', decision);
    const result = await cache.get('test-key');

    expect(result).toEqual(decision);
  });

  test('returns undefined for missing keys', async () => {
    const cache = new MemoryCache();
    const result = await cache.get('nonexistent-key');

    expect(result).toBeUndefined();
  });

  test('respects TTL', async () => {
    const cache = new MemoryCache(100); // 100ms TTL
    const decision: Decision = { allowed: true };

    await cache.set('test-key', decision);

    // Should be present immediately
    let result = await cache.get('test-key');
    expect(result).toEqual(decision);

    // Wait for TTL to expire
    await new Promise((resolve) => setTimeout(resolve, 150));

    // Should be gone after TTL
    result = await cache.get('test-key');
    expect(result).toBeUndefined();
  });

  test('clears all values', async () => {
    const cache = new MemoryCache();
    const decision1: Decision = { allowed: true };
    const decision2: Decision = { allowed: false };

    await cache.set('key1', decision1);
    await cache.set('key2', decision2);

    // Both should be present
    expect(await cache.get('key1')).toEqual(decision1);
    expect(await cache.get('key2')).toEqual(decision2);

    // Clear cache
    await cache.clear();

    // Both should be gone
    expect(await cache.get('key1')).toBeUndefined();
    expect(await cache.get('key2')).toBeUndefined();
  });

  test('overwrites existing values', async () => {
    const cache = new MemoryCache();
    const decision1: Decision = { allowed: true };
    const decision2: Decision = { allowed: false };

    await cache.set('test-key', decision1);
    await cache.set('test-key', decision2);

    const result = await cache.get('test-key');
    expect(result).toEqual(decision2);
  });

  test('handles multiple concurrent gets', async () => {
    const cache = new MemoryCache();
    const decision: Decision = { allowed: true };

    await cache.set('test-key', decision);

    // Multiple concurrent gets
    const results = await Promise.all([
      cache.get('test-key'),
      cache.get('test-key'),
      cache.get('test-key'),
    ]);

    results.forEach((result) => {
      expect(result).toEqual(decision);
    });
  });

  test('handles multiple concurrent sets', async () => {
    const cache = new MemoryCache();
    const decision1: Decision = { allowed: true };
    const decision2: Decision = { allowed: false };

    // Multiple concurrent sets
    await Promise.all([
      cache.set('key1', decision1),
      cache.set('key2', decision2),
      cache.set('key3', decision1),
    ]);

    // All should be present
    expect(await cache.get('key1')).toEqual(decision1);
    expect(await cache.get('key2')).toEqual(decision2);
    expect(await cache.get('key3')).toEqual(decision1);
  });

  test('default TTL is infinite', async () => {
    const cache = new MemoryCache(); // No TTL specified
    const decision: Decision = { allowed: true };

    await cache.set('test-key', decision);

    // Wait a bit
    await new Promise((resolve) => setTimeout(resolve, 100));

    // Should still be present
    const result = await cache.get('test-key');
    expect(result).toEqual(decision);
  });

  test('cleans up expired entries on get', async () => {
    const cache = new MemoryCache(50); // 50ms TTL
    const decision: Decision = { allowed: true };

    // Set multiple values
    await cache.set('key1', decision);
    await cache.set('key2', decision);

    // Wait for expiration
    await new Promise((resolve) => setTimeout(resolve, 100));

    // Get should trigger cleanup
    const result1 = await cache.get('key1');
    expect(result1).toBeUndefined();

    const result2 = await cache.get('key2');
    expect(result2).toBeUndefined();
  });
});
