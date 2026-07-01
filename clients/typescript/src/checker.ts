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
import type { Trace, ExplainOptions } from './trace.js';
import type { UsersetTree, ExpandOptions, Computed } from './expand.js';
import { flattenUsers } from './expand.js';
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
   * Explain returns the resolution tree the authorization engine walked when
   * deciding whether the subject has the relation on the object. The trace
   * shows the proof path on success and every attempted branch on failure —
   * useful for debugging "why doesn't X have Y?" questions.
   *
   * Explain does more work per call than check (it constructs a JSONB trace
   * server-side) so it's intended for debugging and admin flows, not the
   * request-path permission decision.
   *
   * The optional `maxNodes` caps the total nodes in the returned trace. When
   * unset, the cap resolves via the session GUC `melange.max_explain_nodes`,
   * falling back to the server-side default (100). The returned trace's
   * `truncated` flag is set when the cap was hit.
   *
   * The result is parsed straight from the JSONB envelope — snake_case keys
   * are preserved to match the SQL wire format.
   */
  async explain(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    options?: ExplainOptions
  ): Promise<Trace> {
    if (this.validateRequest) {
      validateObject(subject, 'subject');
      validateRelation(relation);
      validateObject(object, 'object');
    }

    const func = prefixIdent('explain_permission', this.databaseSchema);
    const maxNodes =
      options?.maxNodes !== undefined && options.maxNodes > 0
        ? options.maxNodes
        : null;

    // Cast to text on the server so the JSONB arrives as a string we can
    // parse — pg's default JSONB handling returns it already-parsed but the
    // cast keeps behaviour deterministic across pg-versions and driver
    // configurations.
    const result = await this.db.query<{ trace: string | Trace }>(
      `SELECT ${func}($1, $2, $3, $4, $5, $6)::text AS trace`,
      [subject.type, subject.id, relation, object.type, object.id, maxNodes]
    );

    if (!result.rows || result.rows.length === 0) {
      throw new MelangeError('explain_permission returned no rows');
    }

    const raw = result.rows[0].trace;
    if (raw === null || raw === undefined) {
      throw new MelangeError('explain_permission returned null trace');
    }
    return typeof raw === 'string' ? (JSON.parse(raw) as Trace) : raw;
  }

  /**
   * Expand returns the OpenFGA UsersetTree for (object, relation).
   *
   * Resolution is shallow: computed-userset rewrites surface as
   * Leaf.Computed pointers and TTU rewrites surface as
   * Leaf.TupleToUserset pointers. The caller chases pointers with
   * follow-up Expand calls (or uses expandRecursive for the convenience
   * walker that does it automatically).
   *
   * Wildcards (`[type:*]`) and userset references (`[group#member]`)
   * survive inline as user-strings in Leaf.Users — never expanded.
   *
   * The Melange-only `subjectType` and `maxLeaf` options narrow / cap
   * the resulting tree. OpenFGA Expand has neither; capped leaves
   * carry `users_truncated: true` which OpenFGA consumers ignore via
   * JSON's unknown-field handling.
   */
  async expand(
    object: MelangeObject,
    relation: Relation,
    options?: ExpandOptions
  ): Promise<UsersetTree> {
    if (this.validateRequest) {
      validateObject(object, 'object');
      validateRelation(relation);
    }

    const func = prefixIdent('expand_permission', this.databaseSchema);
    const subjectType = options?.subjectType ?? null;
    const maxLeaf =
      options?.maxLeaf !== undefined && options.maxLeaf > 0
        ? options.maxLeaf
        : null;

    const result = await this.db.query<{ tree: string | UsersetTree }>(
      `SELECT ${func}($1, $2, $3, $4, $5)::text AS tree`,
      [object.type, object.id, relation, subjectType, maxLeaf]
    );

    if (!result.rows || result.rows.length === 0) {
      throw new MelangeError('expand_permission returned no rows');
    }

    const raw = result.rows[0].tree;
    if (raw === null || raw === undefined) {
      throw new MelangeError('expand_permission returned null tree');
    }
    return typeof raw === 'string' ? (JSON.parse(raw) as UsersetTree) : raw;
  }

  /**
   * expandRecursive returns the flat, deduplicated list of users with
   * the relation on the object. Issues an initial Expand call then
   * chases every Leaf.Computed and Leaf.TupleToUserset pointer with
   * follow-up Expand calls until the tree is fully resolved.
   *
   * The cost is N round-trips for N pointers — acceptable for
   * admin / debugging flows but not the request hot path; for that,
   * use Checker.listObjects or a regular Check.
   *
   * Cycle-safe: every (object, relation) pair is expanded at most
   * once per call, so a self-referential rewrite (`viewer: viewer from
   * parent` where parent points back at the same object) terminates.
   *
   * Wildcards (`<type>:*`) and userset references
   * (`<type>:<id>#<rel>`) survive as their string forms in the
   * result; callers decide whether to expand them further (the
   * walker doesn't recursively chase userset refs because OpenFGA's
   * shape models them as inline subjects, not as pointers to another
   * (object, relation) pair).
   */
  async expandRecursive(
    object: MelangeObject,
    relation: Relation,
    options?: ExpandOptions
  ): Promise<string[]> {
    const seen = new Set<string>(); // visited (object, relation) pairs
    const users = new Set<string>();

    type Pending = { obj: MelangeObject; rel: Relation };
    const queue: Pending[] = [{ obj: object, rel: relation }];

    while (queue.length > 0) {
      const { obj, rel } = queue.shift()!;
      const key = `${obj.type}:${obj.id}#${rel}`;
      if (seen.has(key)) {
        continue;
      }
      seen.add(key);

      const tree = await this.expand(obj, rel, options);
      // Collect users from this tree.
      for (const u of flattenUsers(tree)) {
        users.add(u);
      }
      // Enqueue pointers for follow-up Expand calls.
      collectPointers(tree.root, queue);
    }

    return Array.from(users).sort();
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

// collectPointers walks an UsersetTree node and queues every
// Leaf.Computed / Leaf.TupleToUserset pointer it finds for follow-up
// Expand calls. Difference subtract is NOT chased — the subtract slot
// names users to exclude, not include (matches flattenUsers behaviour).
function collectPointers(
  node: UsersetTreeNode | null | undefined,
  queue: { obj: MelangeObject; rel: Relation }[]
): void {
  if (!node) return;
  if (node.leaf?.computed) {
    const ptr = parseUsersetPointer(node.leaf.computed);
    if (ptr) queue.push(ptr);
  }
  if (node.leaf?.tuple_to_userset) {
    for (const c of node.leaf.tuple_to_userset.computed) {
      const ptr = parseUsersetPointer(c);
      if (ptr) queue.push(ptr);
    }
  }
  if (node.union) {
    for (const child of node.union.nodes) collectPointers(child, queue);
  }
  if (node.intersection) {
    for (const child of node.intersection.nodes) collectPointers(child, queue);
  }
  if (node.difference) {
    collectPointers(node.difference.base, queue);
  }
}

// parseUsersetPointer splits a `<type>:<id>#<relation>` string into a
// MelangeObject + Relation. Returns null on malformed input so a
// degenerate tree response stops the walker rather than crashing.
function parseUsersetPointer(c: Computed): { obj: MelangeObject; rel: Relation } | null {
  const u = c.userset;
  const hash = u.indexOf('#');
  if (hash < 1 || hash === u.length - 1) return null;
  const colon = u.indexOf(':');
  if (colon < 1 || colon >= hash - 1) return null;
  return {
    obj: { type: u.slice(0, colon), id: u.slice(colon + 1, hash) },
    rel: u.slice(hash + 1),
  };
}

// Re-import the UsersetTreeNode type into module scope so the helper's
// signature can reference it cleanly. (The Checker.expand return type
// already imports UsersetTree which transitively names UsersetTreeNode,
// but the helper signature is local-only so a focused alias keeps the
// typechecker happy without polluting the file's import list.)
type UsersetTreeNode = import('./expand.js').UsersetTreeNode;
