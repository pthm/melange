/**
 * Integration tests for the listObjects object filter.
 *
 * Model: element.view is granted directly to users, or inherited from
 * workspace#view, which is itself inherited from organization#view. Filtering
 * by element#workspace scopes a broad "everything this user can view" query to
 * one workspace.
 */

import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import { Checker } from '../src/checker.js';
import { ValidationError } from '../src/errors.js';
import { createTestPool } from './setup.js';

describe('listObjects object filter', () => {
  let pool: Pool;
  let checker: Checker;

  beforeAll(() => {
    pool = createTestPool();
    checker = new Checker(pool);
  });

  afterAll(async () => {
    await pool.end();
  });

  it('returns every accessible object without a filter', async () => {
    const result = await checker.listObjects({ type: 'user', id: '1' }, 'view', 'element');
    expect(result.items).toHaveLength(1000);
  });

  it('narrows to one workspace', async () => {
    const result = await checker.listObjects({ type: 'user', id: '1' }, 'view', 'element', {
      filter: { relation: 'workspace', subject: { type: 'workspace', id: '7' } },
    });
    expect(result.items).toHaveLength(50);
    for (const id of result.items) {
      expect(Number(id)).toBeGreaterThanOrEqual(7001);
      expect(Number(id)).toBeLessThanOrEqual(7050);
    }
  });

  it('returns nothing for a workspace with no accessible elements', async () => {
    const result = await checker.listObjects({ type: 'user', id: '1' }, 'view', 'element', {
      filter: { relation: 'workspace', subject: { type: 'workspace', id: '999' } },
    });
    expect(result.items).toHaveLength(0);
  });

  // The filter runs before pagination, so a page never leaks outside it.
  it('keeps the filter across pages', async () => {
    const filter = { relation: 'workspace', subject: { type: 'workspace', id: '7' } };
    const page1 = await checker.listObjects({ type: 'user', id: '1' }, 'view', 'element', {
      limit: 20,
      filter,
    });
    expect(page1.items).toHaveLength(20);
    expect(page1.nextCursor).toBeDefined();

    const page2 = await checker.listObjects({ type: 'user', id: '1' }, 'view', 'element', {
      limit: 20,
      after: page1.nextCursor,
      filter,
    });
    expect(page2.items).toHaveLength(20);
    for (const id of page2.items) {
      expect(Number(id)).toBeGreaterThanOrEqual(7001);
      expect(Number(id)).toBeLessThanOrEqual(7050);
    }
    expect(page2.items).not.toEqual(page1.items);
  });

  it('rejects a userset subject before it reaches the database', async () => {
    await expect(
      checker.listObjects({ type: 'user', id: '1' }, 'view', 'element', {
        filter: { relation: 'workspace', subject: { type: 'workspace', id: '7#view' } },
      })
    ).rejects.toThrow(ValidationError);
  });
});
