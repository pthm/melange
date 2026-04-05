/**
 * Melange TypeScript Checker
 *
 * This module provides the Checker class for performing authorization checks
 * against PostgreSQL using Melange's generated SQL functions.
 */

import type {
  MelangeObject,
  Relation,
  ObjectType,
  Decision,
  ContextualTuple,
  PageOptions,
  ListResult,
} from './types.js';
import type { Queryable } from './database.js';
import { Cache, NoopCache } from './cache.js';
import { validateObject, validateRelation } from './validator.js';
import { MelangeError } from './errors.js';
import { BulkCheckBuilder } from './bulk-check.js';
import { prefixIdent } from './identifier.js';

/**
 * CheckerOptions configures a Checker instance.
 */
export interface CheckerOptions {
  /**
   * Cache for storing check results.
   * Default: NoopCache (no caching)
   */
  cache?: Cache;

  /**
   * Decision override for testing.
   * If set, bypasses database checks and always returns this decision.
   * Use DecisionAllow for testing authorized paths, DecisionDeny for unauthorized paths.
   */
  decision?: Decision;

  /**
   * Enable request validation.
   * When true, validates all inputs before executing queries.
   * Default: true
   */
  validateRequest?: boolean;

  /**
   * Enable userset validation.
   * When true, validates userset references in check operations.
   * Default: true
   */
  validateUserset?: boolean;

  /**
   * Sets the database schema where melange objects live.
   */
  databaseSchema?: string;
}

/**
 * Decision constants for testing.
 */
export const DecisionAllow: Decision = { allowed: true };
export const DecisionDeny: Decision = { allowed: false };

/**
 * Checker performs authorization checks against PostgreSQL.
 *
 * Checkers are lightweight and safe to create per-request. They hold no state
 * beyond the database handle, cache, and decision override.
 *
 * @example
 * ```typescript
 * import { Checker } from '@pthm/melange';
 * import { Pool } from 'pg';
 *
 * const pool = new Pool({ connectionString: process.env.DATABASE_URL });
 * const checker = new Checker(pool);
 *
 * const decision = await checker.check(
 *   { type: 'user', id: '123' },
 *   'can_read',
 *   { type: 'repository', id: '456' }
 * );
 * console.log(decision.allowed); // true or false
 * ```
 */
export class Checker {
  private readonly db: Queryable;
  private readonly cache: Cache;
  private readonly decision?: Decision;
  private readonly validateRequest: boolean;
  private readonly validateUserset: boolean;
  private readonly databaseSchema: string;

  /**
   * Creates a new Checker instance.
   *
   * @param db - Database connection (must implement Queryable interface)
   * @param options - Configuration options
   */
  constructor(db: Queryable, options: CheckerOptions = {}) {
    this.db = db;
    this.cache = options.cache ?? new NoopCache();
    this.decision = options.decision;
    this.validateRequest = options.validateRequest ?? true;
    this.validateUserset = options.validateUserset ?? true;
    this.databaseSchema = options.databaseSchema ?? '';
  }

  /**
   * Check performs a permission check.
   *
   * Returns a Decision indicating whether the subject has the specified
   * relation to the object. The check is performed by calling the
   * check_permission function in PostgreSQL.
   *
   * @param subject - The subject requesting access
   * @param relation - The relation to check
   * @param object - The object being accessed
   * @param contextualTuples - Optional tuples to include in this check only
   * @returns A Decision indicating whether access is allowed
   *
   * @example
   * ```typescript
   * // Check if user can read repository
   * const decision = await checker.check(
   *   { type: 'user', id: '123' },
   *   'can_read',
   *   { type: 'repository', id: '456' }
   * );
   *
   * if (decision.allowed) {
   *   // Allow access
   * }
   * ```
   */
  async check(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    contextualTuples?: ContextualTuple[]
  ): Promise<Decision> {
    // Decision override for testing
    if (this.decision) {
      return this.decision;
    }

    // Validate inputs
    if (this.validateRequest) {
      validateObject(subject, 'subject');
      validateRelation(relation);
      validateObject(object, 'object');
    }

    // Check cache (only if no contextual tuples)
    if (!contextualTuples || contextualTuples.length === 0) {
      const cacheKey = this.cacheKey(subject, relation, object);
      const cached = await this.cache.get(cacheKey);
      if (cached !== undefined) {
        return cached;
      }
    }

    const func = prefixIdent('check_permission', this.databaseSchema);

    // Execute check_permission
    // TODO: Support contextual tuples when implemented in PostgreSQL functions
    const result = await this.db.query<{ allowed: number }>(
      `SELECT ${func}($1, $2, $3, $4, $5) as allowed`,
      [subject.type, subject.id, relation, object.type, object.id]
    );

    if (!result.rows || result.rows.length === 0) {
      throw new MelangeError('check_permission returned no rows');
    }

    const allowed = result.rows[0].allowed === 1;
    const decision: Decision = { allowed };

    // Store in cache (only if no contextual tuples)
    if (!contextualTuples || contextualTuples.length === 0) {
      const cacheKey = this.cacheKey(subject, relation, object);
      await this.cache.set(cacheKey, decision);
    }

    return decision;
  }

  /**
   * ListObjects returns all objects of the given type that the subject has the relation to.
   *
   * This is useful for queries like "what repositories can this user read?"
   *
   * @param subject - The subject to check
   * @param relation - The relation to check
   * @param objectType - The type of objects to list
   * @param options - Pagination options
   * @returns List of object IDs
   *
   * @example
   * ```typescript
   * // Get all repositories the user can read
   * const result = await checker.listObjects(
   *   { type: 'user', id: '123' },
   *   'can_read',
   *   'repository',
   *   { limit: 100 }
   * );
   *
   * for (const id of result.items) {
   *   console.log(`Repository ${id}`);
   * }
   *
   * // Get next page if available
   * if (result.nextCursor) {
   *   const nextPage = await checker.listObjects(
   *     { type: 'user', id: '123' },
   *     'can_read',
   *     'repository',
   *     { limit: 100, after: result.nextCursor }
   *   );
   * }
   * ```
   */
  async listObjects(
    subject: MelangeObject,
    relation: Relation,
    objectType: ObjectType,
    options?: PageOptions
  ): Promise<ListResult<string>> {
    if (this.validateRequest) {
      validateObject(subject, 'subject');
      validateRelation(relation);
      if (!objectType) {
        throw new MelangeError('objectType is required');
      }
    }

    let limit = options?.limit ?? null;
    if (typeof limit === 'number' && limit <= 0) {
      limit = null;
    }

    const after = options?.after;

    const func = prefixIdent('list_accessible_objects', this.databaseSchema);

    const result = await this.db.query<{ object_id: string; next_cursor: string }>(
      `SELECT * FROM ${func}($1, $2, $3, $4, $5, $6)`,
      [subject.type, subject.id, relation, objectType, limit, after ?? null]
    );

    const items = result.rows.map((row) => row.object_id);
    const nextCursor = result.rows.length > 0
      ? result.rows[result.rows.length - 1].next_cursor
      : undefined;

    return { items, nextCursor };
  }

  /**
   * ListSubjects returns all subjects of the given type that have the relation to the object.
   *
   * This is useful for queries like "what users can read this repository?"
   *
   * @param subjectType - The type of subjects to list
   * @param relation - The relation to check
   * @param object - The object to check
   * @param options - Pagination options
   * @returns List of subject IDs
   *
   * @example
   * ```typescript
   * // Get all users who can read a repository
   * const result = await checker.listSubjects(
   *   'user',
   *   'can_read',
   *   { type: 'repository', id: '456' },
   *   { limit: 100 }
   * );
   *
   * for (const id of result.items) {
   *   console.log(`User ${id} can read repository`);
   * }
   * ```
   */
  async listSubjects(
    subjectType: ObjectType,
    relation: Relation,
    object: MelangeObject,
    options?: PageOptions
  ): Promise<ListResult<string>> {
    if (this.validateRequest) {
      if (!subjectType) {
        throw new MelangeError('subjectType is required');
      }
      validateRelation(relation);
      validateObject(object, 'object');
    }

    let limit = options?.limit ?? null;
    if (typeof limit === 'number' && limit <= 0) {
      limit = null;
    }

    const after = options?.after;

    const func = prefixIdent('list_accessible_subjects', this.databaseSchema);

    const result = await this.db.query<{ subject_id: string; next_cursor: string }>(
      `SELECT * FROM ${func}($1, $2, $3, $4, $5, $6)`,
      [object.type, object.id, relation, subjectType, limit, after ?? null]
    );

    const items = result.rows.map((row) => row.subject_id);
    const nextCursor = result.rows.length > 0
      ? result.rows[result.rows.length - 1].next_cursor
      : undefined;

    return { items, nextCursor };
  }

  /**
   * CheckWithContextualTuples performs a permission check with contextual tuples.
   *
   * This is a convenience method that calls check() with contextual tuples.
   * Contextual tuples are not persisted and only affect this single check.
   *
   * @param subject - The subject requesting access
   * @param relation - The relation to check
   * @param object - The object being accessed
   * @param contextualTuples - Tuples to include in this check only
   * @returns A Decision indicating whether access is allowed
   *
   * @example
   * ```typescript
   * // Check with temporary permission
   * const decision = await checker.checkWithContextualTuples(
   *   { type: 'user', id: '123' },
   *   'can_read',
   *   { type: 'document', id: '789' },
   *   [
   *     {
   *       subject: { type: 'user', id: '123' },
   *       relation: 'temp_access',
   *       object: { type: 'document', id: '789' }
   *     }
   *   ]
   * );
   * ```
   */
  async checkWithContextualTuples(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    contextualTuples: ContextualTuple[]
  ): Promise<Decision> {
    return this.check(subject, relation, object, contextualTuples);
  }

  /**
   * Create a new BulkCheckBuilder for batching multiple permission checks
   * into a single SQL call via check_permission_bulk.
   *
   * @example
   * ```typescript
   * const results = await checker.newBulkCheck()
   *   .add(user, 'can_read', repo1)
   *   .add(user, 'can_read', repo2)
   *   .execute();
   *
   * if (results.all()) {
   *   // All checks passed
   * }
   * ```
   */
  newBulkCheck(): BulkCheckBuilder {
    return new BulkCheckBuilder({
      db: this.db,
      cache: this.cache,
      decision: this.decision,
      shouldValidate: this.validateRequest,
      databaseSchema: this.databaseSchema,
      cacheKey: this.cacheKey.bind(this),
    });
  }

  /**
   * Generate a cache key for a check operation.
   */
  private cacheKey(subject: MelangeObject, relation: Relation, object: MelangeObject): string {
    return `${subject.type}:${subject.id}|${relation}|${object.type}:${object.id}`;
  }
}
