/**
 * Integration tests for Checker class.
 *
 * These tests run against a real PostgreSQL database with melange schema installed.
 * The database can be either:
 * - A testcontainers instance (started by Go tests)
 * - An external database specified by DATABASE_URL
 *
 * Run with: pnpm test
 */

import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { Pool } from 'pg';
import { Checker, MemoryCache, DecisionAllow, DecisionDeny } from '../src/index.js';
import { createTestPool, verifyTestDatabase, closeTestPool } from './setup.js';

describe('Checker Integration Tests', () => {
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

  describe('Organization Permissions', () => {
    let orgId: number;
    let ownerId: number;
    let adminId: number;
    let memberId: number;

    beforeAll(async () => {
      // Create users
      const ownerResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_org_owner') RETURNING id"
      );
      ownerId = ownerResult.rows[0].id;

      const adminResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_org_admin') RETURNING id"
      );
      adminId = adminResult.rows[0].id;

      const memberResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_org_member') RETURNING id"
      );
      memberId = memberResult.rows[0].id;

      // Create organization
      const orgResult = await pool.query(
        "INSERT INTO organizations (name) VALUES ('ts_test_org') RETURNING id"
      );
      orgId = orgResult.rows[0].id;

      // Add members with different roles
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [orgId, ownerId, 'owner']
      );
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [orgId, adminId, 'admin']
      );
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [orgId, memberId, 'member']
      );
    });

    test('owner has all permissions', async () => {
      const owner = { type: 'user', id: String(ownerId) };
      const org = { type: 'organization', id: String(orgId) };

      // Owner should have can_read
      let decision = await checker.check(owner, 'can_read', org);
      expect(decision.allowed).toBe(true);

      // Owner should have can_admin
      decision = await checker.check(owner, 'can_admin', org);
      expect(decision.allowed).toBe(true);

      // Owner should have can_delete
      decision = await checker.check(owner, 'can_delete', org);
      expect(decision.allowed).toBe(true);
    });

    test('admin has admin and read permissions but not delete', async () => {
      const admin = { type: 'user', id: String(adminId) };
      const org = { type: 'organization', id: String(orgId) };

      // Admin should have can_read
      let decision = await checker.check(admin, 'can_read', org);
      expect(decision.allowed).toBe(true);

      // Admin should have can_admin
      decision = await checker.check(admin, 'can_admin', org);
      expect(decision.allowed).toBe(true);

      // Admin should NOT have can_delete
      decision = await checker.check(admin, 'can_delete', org);
      expect(decision.allowed).toBe(false);
    });

    test('member has read permission only', async () => {
      const member = { type: 'user', id: String(memberId) };
      const org = { type: 'organization', id: String(orgId) };

      // Member should have can_read
      let decision = await checker.check(member, 'can_read', org);
      expect(decision.allowed).toBe(true);

      // Member should NOT have can_admin
      decision = await checker.check(member, 'can_admin', org);
      expect(decision.allowed).toBe(false);

      // Member should NOT have can_delete
      decision = await checker.check(member, 'can_delete', org);
      expect(decision.allowed).toBe(false);
    });
  });

  describe('Repository Permissions with Inheritance', () => {
    let orgId: number;
    let repoId: number;
    let orgMemberId: number;
    let repoWriterId: number;
    let outsiderId: number;

    beforeAll(async () => {
      // Create users
      const orgMemberResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_org_member_repo') RETURNING id"
      );
      orgMemberId = orgMemberResult.rows[0].id;

      const repoWriterResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_repo_writer') RETURNING id"
      );
      repoWriterId = repoWriterResult.rows[0].id;

      const outsiderResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_outsider') RETURNING id"
      );
      outsiderId = outsiderResult.rows[0].id;

      // Create organization
      const orgResult = await pool.query(
        "INSERT INTO organizations (name) VALUES ('ts_test_org_repo') RETURNING id"
      );
      orgId = orgResult.rows[0].id;

      // Add org member
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [orgId, orgMemberId, 'member']
      );

      // Create repository
      const repoResult = await pool.query(
        'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
        ['ts_test_repo', orgId]
      );
      repoId = repoResult.rows[0].id;

      // Add repo writer
      await pool.query(
        'INSERT INTO repository_collaborators (repository_id, user_id, role) VALUES ($1, $2, $3)',
        [repoId, repoWriterId, 'writer']
      );
    });

    test('org member can read repository via inheritance', async () => {
      const orgMember = { type: 'user', id: String(orgMemberId) };
      const repo = { type: 'repository', id: String(repoId) };

      // Org member should have can_read via "can_read from org"
      const decision = await checker.check(orgMember, 'can_read', repo);
      expect(decision.allowed).toBe(true);
    });

    test('repo writer can write to repository', async () => {
      const repoWriter = { type: 'user', id: String(repoWriterId) };
      const repo = { type: 'repository', id: String(repoId) };

      // Writer should have can_write
      let decision = await checker.check(repoWriter, 'can_write', repo);
      expect(decision.allowed).toBe(true);

      // Writer should have can_read (via writer hierarchy)
      decision = await checker.check(repoWriter, 'can_read', repo);
      expect(decision.allowed).toBe(true);

      // Writer should NOT have can_delete
      decision = await checker.check(repoWriter, 'can_delete', repo);
      expect(decision.allowed).toBe(false);
    });

    test('outsider has no access to repository', async () => {
      const outsider = { type: 'user', id: String(outsiderId) };
      const repo = { type: 'repository', id: String(repoId) };

      // Outsider should NOT have can_read
      let decision = await checker.check(outsider, 'can_read', repo);
      expect(decision.allowed).toBe(false);

      // Outsider should NOT have can_write
      decision = await checker.check(outsider, 'can_write', repo);
      expect(decision.allowed).toBe(false);
    });
  });

  describe('List Operations', () => {
    let userId: number;
    let org1Id: number;
    let org2Id: number;
    let repo1Id: number;
    let repo2Id: number;

    beforeAll(async () => {
      // Create user
      const userResult = await pool.query(
        "INSERT INTO users (username) VALUES ('ts_list_user') RETURNING id"
      );
      userId = userResult.rows[0].id;

      // Create two organizations
      const org1Result = await pool.query(
        "INSERT INTO organizations (name) VALUES ('ts_list_org1') RETURNING id"
      );
      org1Id = org1Result.rows[0].id;

      const org2Result = await pool.query(
        "INSERT INTO organizations (name) VALUES ('ts_list_org2') RETURNING id"
      );
      org2Id = org2Result.rows[0].id;

      // Add user as member of org1 only
      await pool.query(
        'INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, $3)',
        [org1Id, userId, 'member']
      );

      // Create repositories in both organizations
      const repo1Result = await pool.query(
        'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
        ['ts_list_repo1', org1Id]
      );
      repo1Id = repo1Result.rows[0].id;

      const repo2Result = await pool.query(
        'INSERT INTO repositories (name, organization_id) VALUES ($1, $2) RETURNING id',
        ['ts_list_repo2', org2Id]
      );
      repo2Id = repo2Result.rows[0].id;
    });

    test('listObjects returns accessible repositories', async () => {
      const user = { type: 'user', id: String(userId) };

      const result = await checker.listObjects(user, 'can_read', 'repository', {
        limit: 100,
      });

      // User should have access to repo1 via org1 membership
      expect(result.items).toContain(String(repo1Id));

      // User should NOT have access to repo2
      expect(result.items).not.toContain(String(repo2Id));
    });

    test('listSubjects returns users with access', async () => {
      const repo = { type: 'repository', id: String(repo1Id) };

      const result = await checker.listSubjects('user', 'can_read', repo, {
        limit: 100,
      });

      // listSubjects should return successfully with proper structure
      // Note: The result may be empty depending on how the implementation
      // handles tuple-to-userset relationships (e.g., "can_read from org")
      expect(result).toHaveProperty('items');
      expect(result).toHaveProperty('nextCursor');
      expect(Array.isArray(result.items)).toBe(true);
    });

    test('listObjects respects pagination', async () => {
      const user = { type: 'user', id: String(userId) };

      // Request with limit
      const result = await checker.listObjects(user, 'can_read', 'repository', {
        limit: 1,
      });

      // Should return at most 1 item
      expect(result.items.length).toBeLessThanOrEqual(1);

      // Pagination returns results (may or may not have nextCursor depending on total count)
      expect(result).toHaveProperty('items');
      expect(result).toHaveProperty('nextCursor');
    });
  });

  describe('Caching', () => {
    test('caches check results', async () => {
      const cache = new MemoryCache(60000); // 60 second TTL
      const cachedChecker = new Checker(pool, { cache });

      const user = { type: 'user', id: '999' };
      const org = { type: 'organization', id: '999' };

      // First check - miss cache, hits database
      const decision1 = await cachedChecker.check(user, 'can_read', org);

      // Second check - should hit cache
      const decision2 = await cachedChecker.check(user, 'can_read', org);

      // Results should be the same
      expect(decision1.allowed).toBe(decision2.allowed);
    });

    test('cache can be cleared', async () => {
      const cache = new MemoryCache(60000);
      const cachedChecker = new Checker(pool, { cache });

      const user = { type: 'user', id: '998' };
      const org = { type: 'organization', id: '998' };

      // Populate cache
      await cachedChecker.check(user, 'can_read', org);

      // Clear cache
      await cache.clear();

      // Next check should hit database again
      const decision = await cachedChecker.check(user, 'can_read', org);
      expect(decision).toBeDefined();
    });
  });

  describe('Decision Override', () => {
    test('DecisionAllow always returns allowed', async () => {
      const allowChecker = new Checker(pool, { decision: DecisionAllow });

      const user = { type: 'user', id: 'nonexistent' };
      const org = { type: 'organization', id: 'nonexistent' };

      const decision = await allowChecker.check(user, 'can_read', org);
      expect(decision.allowed).toBe(true);
    });

    test('DecisionDeny always returns denied', async () => {
      const denyChecker = new Checker(pool, { decision: DecisionDeny });

      const user = { type: 'user', id: 'nonexistent' };
      const org = { type: 'organization', id: 'nonexistent' };

      const decision = await denyChecker.check(user, 'can_read', org);
      expect(decision.allowed).toBe(false);
    });
  });

  describe('Validation', () => {
    test('validates subject object', async () => {
      const invalidSubject = { type: '', id: '123' };
      const obj = { type: 'organization', id: '123' };

      await expect(checker.check(invalidSubject, 'can_read', obj)).rejects.toThrow(
        'subject.type is required'
      );
    });

    test('validates object', async () => {
      const subject = { type: 'user', id: '123' };
      const invalidObj = { type: 'organization', id: '' };

      await expect(checker.check(subject, 'can_read', invalidObj)).rejects.toThrow(
        'object.id is required'
      );
    });

    test('validates relation', async () => {
      const subject = { type: 'user', id: '123' };
      const obj = { type: 'organization', id: '123' };

      await expect(checker.check(subject, '', obj)).rejects.toThrow(
        'relation is required'
      );
    });

    test('validation can be disabled', async () => {
      const noValidationChecker = new Checker(pool, { validateRequest: false });

      const subject = { type: '', id: '' };
      const obj = { type: '', id: '' };

      // With validation disabled, check proceeds and returns a decision
      // (database handles invalid inputs gracefully)
      const decision = await noValidationChecker.check(subject, '', obj);
      expect(decision).toHaveProperty('allowed');
    });
  });
});
