/**
 * Integration tests for Checker.explain.
 *
 * Mirrors the Go test/explain_integration_test.go suite — each scenario
 * inserts the minimum tuples needed, calls Checker.explain through the
 * TypeScript client, and asserts the wire-format Trace shape so the
 * server-side JSONB → JS object pipeline is exercised end-to-end. The
 * shared melange schema in test/testutil/testdata/schema.fga is the source
 * of truth for the relations used here.
 */

import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import { Checker } from '../src/index.js';
import type { Trace, TraceNode } from '../src/trace.js';
import { createTestPool, verifyTestDatabase, closeTestPool } from './setup.js';

describe('Checker.explain integration', () => {
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

  // findChild walks the children of a node and returns the first one of the
  // requested type. Used to assert "this branch was attempted" without
  // pinning the position of the branch (which is renderer/ordering noise).
  const findChild = (node: TraceNode, type: TraceNode['type']): TraceNode | undefined =>
    (node.children ?? []).find((c) => c.type === type);

  test('direct grant success exposes evidence tuple', async () => {
    const ownerRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_owner') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_org') RETURNING id"
    );
    const ownerId = String(ownerRow.rows[0].id);
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, ownerId, 'owner']
    );

    const trace: Trace = await checker.explain(
      { type: 'user', id: ownerId },
      'owner',
      { type: 'organization', id: orgId }
    );

    expect(trace.object).toBe(`organization:${orgId}`);
    expect(trace.relation).toBe('owner');
    expect(trace.subject).toBe(`user:${ownerId}`);
    expect(trace.result).toBe(true);
    expect(trace.root).not.toBeNull();
    expect(trace.root!.type).toBe('direct');
    expect(trace.root!.evidence).toHaveLength(1);
    const ev = trace.root!.evidence![0];
    expect(ev.subject_type).toBe('user');
    expect(ev.subject_id).toBe(ownerId);
    expect(ev.relation).toBe('owner');
    expect(ev.object_type).toBe('organization');
    expect(ev.object_id).toBe(orgId);
    // node_count is informational but should be at least one for a success.
    expect(trace.node_count ?? 0).toBeGreaterThan(0);
  });

  test('direct grant failure records a NodeDirect attempt under NodeUnion', async () => {
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_outsider') RETURNING id"
    );
    const ownerRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_actual_owner') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_other_org') RETURNING id"
    );
    const userId = String(userRow.rows[0].id);
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, ownerRow.rows[0].id, 'owner']
    );

    const trace = await checker.explain(
      { type: 'user', id: userId },
      'owner',
      { type: 'organization', id: orgId }
    );

    expect(trace.result).toBe(false);
    expect(trace.root).not.toBeNull();
    expect(trace.root!.type).toBe('union');
    const directFailure = findChild(trace.root!, 'direct');
    expect(directFailure).toBeDefined();
    expect(directFailure!.result).toBe(false);
  });

  test('TTU success returns NodeTTU with linking label', async () => {
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_ttu_alice') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_ttu_org') RETURNING id"
    );
    const userId = String(userRow.rows[0].id);
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, userId, 'owner']
    );
    const repoRow = await pool.query<{ id: number }>(
      'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
      ['ts_explain_ttu_repo', orgId]
    );
    const repoId = String(repoRow.rows[0].id);

    const trace = await checker.explain(
      { type: 'user', id: userId },
      'can_deploy',
      { type: 'repository', id: repoId }
    );

    expect(trace.result).toBe(true);
    expect(trace.root).not.toBeNull();
    expect(trace.root!.type).toBe('ttu');
    expect(trace.root!.label ?? '').toContain('via org →');
    expect(trace.root!.label ?? '').toContain('can_admin');
    expect(trace.root!.children).toHaveLength(1);
  });

  test('wildcard sentinel surfaces SubjectRef with id "*"', async () => {
    const aliceRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_wild_alice') RETURNING id"
    );
    const ownerRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_wild_owner') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_wild_org') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, ownerRow.rows[0].id, 'owner']
    );
    const repoRow = await pool.query<{ id: number }>(
      'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
      ['ts_explain_wild_repo', orgId]
    );
    const repoId = String(repoRow.rows[0].id);
    await pool.query(
      'INSERT INTO repository_bans (repository_id, banned_all) VALUES ($1, true)',
      [repoId]
    );

    const trace = await checker.explain(
      { type: 'user', id: String(aliceRow.rows[0].id) },
      'banned',
      { type: 'repository', id: repoId }
    );

    expect(trace.result).toBe(true);
    expect(trace.root).not.toBeNull();
    expect(trace.root!.type).toBe('wildcard');
    expect(trace.root!.users).toHaveLength(1);
    expect(trace.root!.users![0].type).toBe('user');
    expect(trace.root!.users![0].id).toBe('*');
  });

  test('maxNodes=1 flips the truncated envelope flag', async () => {
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_trunc_user') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_trunc_org') RETURNING id"
    );
    const orgId = String(orgRow.rows[0].id);
    const repoRow = await pool.query<{ id: number }>(
      'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
      ['ts_explain_trunc_repo', orgId]
    );

    const trace = await checker.explain(
      { type: 'user', id: String(userRow.rows[0].id) },
      'can_deploy',
      { type: 'repository', id: String(repoRow.rows[0].id) },
      { maxNodes: 1 }
    );

    expect(trace.truncated).toBe(true);
    // node_count should reflect at least the truncation node itself.
    expect(trace.node_count ?? 0).toBeGreaterThan(0);
  });

  test('unknown relation returns the unsupported sentinel envelope', async () => {
    // Bypass client validation by disabling validateRelation — the relation
    // exists as a string but the dispatcher has no entry for it. The
    // server-side sentinel is the SUT here, not the validator.
    const noValidate = new Checker(pool, { validateRequest: false });
    const trace = await noValidate.explain(
      { type: 'user', id: '1' },
      'nonexistent_relation',
      { type: 'widget', id: '42' }
    );
    expect(trace.result).toBe(false);
    expect(trace.root).not.toBeNull();
    expect(trace.root!.type).toBe('union');
    expect(trace.root!.label ?? '').toContain('explain not yet supported');
  });

  test('explain agrees with check on the same subject/relation/object', async () => {
    // Cross-check invariant: every Explain result must equal the boolean
    // Check returns for the same inputs. Drift here means the trace lies.
    const userRow = await pool.query<{ id: number }>(
      "INSERT INTO users (username) VALUES ('ts_explain_parity_user') RETURNING id"
    );
    const orgRow = await pool.query<{ id: number }>(
      "INSERT INTO organizations (name) VALUES ('ts_explain_parity_org') RETURNING id"
    );
    const userId = String(userRow.rows[0].id);
    const orgId = String(orgRow.rows[0].id);
    await pool.query(
      'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
      [orgId, userId, 'admin']
    );

    for (const relation of ['can_read', 'can_admin', 'can_delete']) {
      const decision = await checker.check(
        { type: 'user', id: userId },
        relation,
        { type: 'organization', id: orgId }
      );
      const trace = await checker.explain(
        { type: 'user', id: userId },
        relation,
        { type: 'organization', id: orgId }
      );
      expect(
        trace.result,
        `explain vs check disagree for ${relation}: check=${decision.allowed}, explain=${trace.result}`
      ).toBe(decision.allowed);
    }
  });
});
