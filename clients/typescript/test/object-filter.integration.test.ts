/**
 * Integration tests for the listObjects object filter.
 *
 * Model (from the shared test schema): repository.can_read is reachable via
 * `can_read from org`, where `org` is a plain, directly-assignable relation
 * from repository to organization. Filtering repository#org scopes a broad
 * "every repository this user can read" query to one organization.
 */

import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import { Checker } from '../src/checker.js';
import { ValidationError } from '../src/errors.js';
import { createTestPool } from './setup.js';

describe('listObjects object filter', () => {
  let pool: Pool;
  let checker: Checker;
  let userId: string;
  let mainOrgId: string;
  let targetOrgId: string;
  let otherOrgId: string;
  let targetRepoIds: Set<string>;

  beforeAll(async () => {
    pool = createTestPool();
    checker = new Checker(pool);

    const userResult = await pool.query(
      "INSERT INTO users (username) VALUES ('ts_object_filter_user') RETURNING id"
    );
    userId = String(userResult.rows[0].id);

    const orgs = await pool.query(
      `INSERT INTO organizations (name)
       VALUES ('ts_object_filter_main'), ('ts_object_filter_target'), ('ts_object_filter_other')
       RETURNING id`
    );
    [mainOrgId, targetOrgId, otherOrgId] = orgs.rows.map((r) => String(r.id));

    // The user is a member of main and target, but not other, so repositories
    // under "other" exist (and are filterable) without being accessible.
    await pool.query(
      `INSERT INTO organization_members (organization_id, user_id, role)
       VALUES ($1, $2, 'member'), ($3, $2, 'member')`,
      [mainOrgId, userId, targetOrgId]
    );

    // 950 in main + 50 in target = 1000 accessible repositories overall.
    await pool.query(
      `INSERT INTO repositories (organization_id, name)
       SELECT $1, 'main-repo-' || g FROM generate_series(1, 950) AS g`,
      [mainOrgId]
    );
    const targetRepos = await pool.query(
      `INSERT INTO repositories (organization_id, name)
       SELECT $1, 'target-repo-' || g FROM generate_series(1, 50) AS g
       RETURNING id`,
      [targetOrgId]
    );
    targetRepoIds = new Set(targetRepos.rows.map((r) => String(r.id)));

    await pool.query(
      `INSERT INTO repositories (organization_id, name)
       SELECT $1, 'other-repo-' || g FROM generate_series(1, 5) AS g`,
      [otherOrgId]
    );
  });

  afterAll(async () => {
    await pool.end();
  });

  it('returns every accessible object without a filter', async () => {
    const result = await checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository');
    expect(result.items).toHaveLength(1000);
  });

  it('narrows to one organization', async () => {
    const result = await checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository', {
      filter: { relation: 'org', subject: { type: 'organization', id: targetOrgId } },
    });
    expect(result.items).toHaveLength(50);
    for (const id of result.items) {
      expect(targetRepoIds.has(id)).toBe(true);
    }
  });

  it('returns nothing for an organization with no accessible repositories', async () => {
    const result = await checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository', {
      filter: { relation: 'org', subject: { type: 'organization', id: otherOrgId } },
    });
    expect(result.items).toHaveLength(0);
  });

  // The filter runs before pagination, so a page never leaks outside it.
  it('keeps the filter across pages', async () => {
    const filter = { relation: 'org', subject: { type: 'organization', id: targetOrgId } };
    const page1 = await checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository', {
      limit: 20,
      filter,
    });
    expect(page1.items).toHaveLength(20);
    expect(page1.nextCursor).toBeDefined();

    const page2 = await checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository', {
      limit: 20,
      after: page1.nextCursor,
      filter,
    });
    expect(page2.items).toHaveLength(20);
    for (const id of page2.items) {
      expect(targetRepoIds.has(id)).toBe(true);
    }
    expect(page2.items).not.toEqual(page1.items);
  });

  it('rejects a userset subject before it reaches the database', async () => {
    await expect(
      checker.listObjects({ type: 'user', id: userId }, 'can_read', 'repository', {
        filter: { relation: 'org', subject: { type: 'organization', id: `${targetOrgId}#member` } },
      })
    ).rejects.toThrow(ValidationError);
  });
});
