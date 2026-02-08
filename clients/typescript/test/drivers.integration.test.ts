/**
 * Driver compatibility integration tests.
 *
 * Verifies that the melange Checker works correctly with multiple PostgreSQL
 * client libraries: pg (node-postgres) and postgres.js.
 */

import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import postgres from 'postgres';
import { Checker, postgresAdapter } from '../src/index.js';
import type { Queryable } from '../src/database.js';
import {
  getDatabaseURL,
  verifyTestDatabaseWithQueryable,
} from './setup.js';

/**
 * Shared test data setup and assertions for each driver.
 * Each driver creates its own data with a unique prefix to avoid collisions.
 */
async function setupTestData(db: Queryable, prefix: string) {
  const ownerResult = await db.query<{ id: number }>(
    `INSERT INTO users (username) VALUES ($1) RETURNING id`,
    [`${prefix}_owner`]
  );
  const ownerId = ownerResult.rows[0].id;

  const memberResult = await db.query<{ id: number }>(
    `INSERT INTO users (username) VALUES ($1) RETURNING id`,
    [`${prefix}_member`]
  );
  const memberId = memberResult.rows[0].id;

  const outsiderResult = await db.query<{ id: number }>(
    `INSERT INTO users (username) VALUES ($1) RETURNING id`,
    [`${prefix}_outsider`]
  );
  const outsiderId = outsiderResult.rows[0].id;

  const orgResult = await db.query<{ id: number }>(
    `INSERT INTO organizations (name) VALUES ($1) RETURNING id`,
    [`${prefix}_org`]
  );
  const orgId = orgResult.rows[0].id;

  await db.query(
    'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
    [orgId, ownerId, 'owner']
  );
  await db.query(
    'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
    [orgId, memberId, 'member']
  );

  const repoResult = await db.query<{ id: number }>(
    'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
    [`${prefix}_repo`, orgId]
  );
  const repoId = repoResult.rows[0].id;

  return { ownerId, memberId, outsiderId, orgId, repoId };
}

function runDriverTests(getChecker: () => Checker, getData: () => ReturnType<typeof setupTestData> extends Promise<infer T> ? T : never) {
  test('Check (allow)', async () => {
    const { ownerId, orgId } = getData();
    const owner = { type: 'user', id: String(ownerId) };
    const org = { type: 'organization', id: String(orgId) };

    const decision = await getChecker().check(owner, 'can_read', org);
    expect(decision.allowed).toBe(true);
  });

  test('Check (deny)', async () => {
    const { outsiderId, orgId } = getData();
    const outsider = { type: 'user', id: String(outsiderId) };
    const org = { type: 'organization', id: String(orgId) };

    const decision = await getChecker().check(outsider, 'can_read', org);
    expect(decision.allowed).toBe(false);
  });

  test('ListObjects', async () => {
    const { memberId, repoId } = getData();
    const member = { type: 'user', id: String(memberId) };

    const result = await getChecker().listObjects(member, 'can_read', 'repository', {
      limit: 100,
    });

    expect(result.items).toContain(String(repoId));
  });

  test('ListSubjects', async () => {
    const { ownerId, memberId, outsiderId, orgId } = getData();
    const org = { type: 'organization', id: String(orgId) };

    const result = await getChecker().listSubjects('user', 'can_read', org, {
      limit: 100,
    });

    expect(result.items).toContain(String(ownerId));
    expect(result.items).toContain(String(memberId));
    expect(result.items).not.toContain(String(outsiderId));
  });

  test('BulkCheck', async () => {
    const { ownerId, outsiderId, orgId } = getData();
    const owner = { type: 'user', id: String(ownerId) };
    const outsider = { type: 'user', id: String(outsiderId) };
    const org = { type: 'organization', id: String(orgId) };

    const results = await getChecker()
      .newBulkCheck()
      .add(owner, 'can_read', org)
      .add(outsider, 'can_read', org)
      .execute();

    expect(results.length).toBe(2);
    expect(results.get(0).allowed).toBe(true);
    expect(results.get(1).allowed).toBe(false);
  });
}

describe('Driver Compatibility', () => {
  describe('pg (node-postgres)', () => {
    let pool: Pool;
    let checker: Checker;
    let data: Awaited<ReturnType<typeof setupTestData>>;

    beforeAll(async () => {
      pool = new Pool({
        connectionString: getDatabaseURL(),
        max: 5,
      });
      await verifyTestDatabaseWithQueryable(pool);
      checker = new Checker(pool);
      data = await setupTestData(pool, 'drv_pg');
    });

    afterAll(async () => {
      await pool.end();
    });

    runDriverTests(() => checker, () => data);
  });

  describe('postgres.js', () => {
    let sql: postgres.Sql;
    let queryable: Queryable;
    let checker: Checker;
    let data: Awaited<ReturnType<typeof setupTestData>>;

    beforeAll(async () => {
      sql = postgres(getDatabaseURL(), { max: 5 });
      queryable = postgresAdapter(sql);
      await verifyTestDatabaseWithQueryable(queryable);
      checker = new Checker(queryable);
      data = await setupTestData(queryable, 'drv_postgresjs');
    });

    afterAll(async () => {
      await sql.end();
    });

    runDriverTests(() => checker, () => data);
  });
});
