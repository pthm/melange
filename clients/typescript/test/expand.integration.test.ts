/**
 * Integration tests for Checker.expand and Checker.expandRecursive.
 *
 * Mirrors the Go test/expand_integration_test.go scenarios — each test
 * inserts the minimum tuples needed, calls the TS client, and asserts
 * the wire-format UsersetTree shape so the server-side JSONB → JS
 * object pipeline is exercised end-to-end. The shared schema in
 * test/testutil/testdata/schema.fga is the source of truth for the
 * relations used here.
 */

import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import { Checker, flattenUsers } from '../src/index.js';
import type { UsersetTree } from '../src/index.js';
import { createTestPool, verifyTestDatabase, closeTestPool } from './setup.js';

describe('Checker.expand integration', () => {
  let pool: Pool;
  let checker: Checker;

  beforeAll(async () => {
    pool = createTestPool();
    await verifyTestDatabase(pool);
    checker = new Checker(pool);
  });

  afterAll(async () => {
    await closeTestPool(pool);
  });

  test('direct grant returns Leaf.Users with concrete user', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_direct_org') RETURNING id"
    );
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_direct_user') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    const userId = String(userRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, userId, 'owner']
    );

    const tree: UsersetTree = await checker.expand(
      { type: 'organization', id: orgId },
      'owner'
    );
    expect(tree.root).not.toBeNull();
    expect(tree.root!.name).toBe(`organization:${orgId}#owner`);
    expect(tree.root!.leaf).toBeDefined();
    expect(tree.root!.leaf!.users).toBeDefined();
    expect(tree.root!.leaf!.users!.users).toEqual([`user:${userId}`]);
    // users_truncated omitted on uncapped responses
    expect(tree.root!.leaf!.users!.users_truncated).toBeUndefined();
    // flattenUsers returns the same payload for a single-leaf tree
    expect(flattenUsers(tree)).toEqual([`user:${userId}`]);
  });

  test('multi-rewrite relation emits union of children', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_union_org') RETURNING id"
    );
    const directRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_union_admin') RETURNING id"
    );
    const ownerRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_union_owner') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3), ($1, $4, $5)',
      [orgId, directRow.rows[0].id, 'admin', ownerRow.rows[0].id, 'owner']
    );

    const tree = await checker.expand(
      { type: 'organization', id: orgId },
      'admin'
    );
    expect(tree.root!.union).toBeDefined();
    expect(tree.root!.union!.nodes).toHaveLength(2);
    // One child carries the direct Leaf.Users, the other a Computed
    // pointer to the implied owner relation.
    const hasComputed = tree.root!.union!.nodes.some(
      (n) => n.leaf?.computed?.userset === `organization:${orgId}#owner`
    );
    const hasDirect = tree.root!.union!.nodes.some(
      (n) => n.leaf?.users?.users.includes(`user:${directRow.rows[0].id}`)
    );
    expect(hasComputed).toBe(true);
    expect(hasDirect).toBe(true);
    // flattenUsers includes only the direct admin (Computed pointer not chased)
    expect(flattenUsers(tree)).toEqual([`user:${directRow.rows[0].id}`]);
  });

  test('subjectType filter narrows Leaf.Users', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_filter_org') RETURNING id"
    );
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_filter_user') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, userRow.rows[0].id, 'owner']
    );

    // Baseline: no filter — user appears.
    const full = await checker.expand(
      { type: 'organization', id: orgId },
      'owner'
    );
    expect(full.root!.leaf!.users!.users).toContain(`user:${userRow.rows[0].id}`);

    // Mismatched filter — empty array, but no error.
    const empty = await checker.expand(
      { type: 'organization', id: orgId },
      'owner',
      { subjectType: 'never_a_real_type' }
    );
    expect(empty.root!.leaf!.users!.users).toEqual([]);
  });

  test('maxLeaf cap surfaces users_truncated:true', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_cap_org') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    for (let i = 0; i < 3; i++) {
      const userRow = await pool.query<{ id: number }>(
        `INSERT INTO users (username) VALUES ('ts_cap_owner_${i}') RETURNING id`
      );
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [orgId, userRow.rows[0].id, 'owner']
      );
    }

    // Uncapped — 3 entries, no users_truncated key.
    const full = await checker.expand(
      { type: 'organization', id: orgId },
      'owner'
    );
    expect(full.root!.leaf!.users!.users).toHaveLength(3);
    expect(full.root!.leaf!.users!.users_truncated).toBeUndefined();

    // Capped to 2 — 2 entries, users_truncated:true.
    const capped = await checker.expand(
      { type: 'organization', id: orgId },
      'owner',
      { maxLeaf: 2 }
    );
    expect(capped.root!.leaf!.users!.users).toHaveLength(2);
    expect(capped.root!.leaf!.users!.users_truncated).toBe(true);
  });

  test('wildcard grant emits "user:*" inline in Leaf.Users', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_wild_org') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    const repoRow = await pool.query<{ id: number }>(
      "INSERT INTO repositories (name, organization_id) VALUES ('ts_expand_wild_repo', $1) RETURNING id",
      [orgId]
    );
    const repoId = String(repoRow.rows[0].id);
    await pool.query(
      'INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)',
      [repoId]
    );

    const tree = await checker.expand(
      { type: 'repository', id: repoId },
      'banned'
    );
    expect(tree.root!.leaf!.users!.users).toEqual(['user:*']);
    // flattenUsers includes the wildcard string — consumers treat
    // <type>:* as "every user of that type".
    expect(flattenUsers(tree)).toEqual(['user:*']);
  });

  test('expandRecursive chases Computed pointer and matches list_subjects', async () => {
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_expand_rec_org') RETURNING id"
    );
    const aliceRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_rec_alice') RETURNING id"
    );
    const bobRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_expand_rec_bob') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    const aliceId = String(aliceRow.rows[0].id);
    const bobId = String(bobRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3), ($1, $4, $5)',
      [orgId, aliceId, 'admin', bobId, 'owner']
    );

    // Recursive walker chases the owner pointer and returns both
    // users. Same answer ListSubjects would give via dedicated SQL.
    const users = await checker.expandRecursive(
      { type: 'organization', id: orgId },
      'admin'
    );
    expect(users.sort()).toEqual([`user:${aliceId}`, `user:${bobId}`].sort());

    const listed = await checker.listSubjects(
      'user',
      'admin',
      { type: 'organization', id: orgId }
    );
    expect(users.sort()).toEqual(listed.items.map((id) => `user:${id}`).sort());
  });
});
