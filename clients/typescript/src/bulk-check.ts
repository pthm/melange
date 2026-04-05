/**
 * Bulk permission check API for Melange TypeScript client.
 *
 * This module provides BulkCheckBuilder for batching multiple permission checks
 * into a single SQL call via check_permission_bulk, with deduplication and caching.
 */

import type { MelangeObject, Relation, Decision } from './types.js';
import type { Queryable } from './database.js';
import type { Cache } from './cache.js';
import { MelangeError, BulkCheckDeniedError } from './errors.js';
import { validateObject, validateRelation } from './validator.js';
import { prefixIdent } from './identifier.js';

/**
 * Maximum number of checks allowed in a single bulk operation.
 * Prevents accidental resource exhaustion from unbounded batches.
 */
export const MAX_BULK_CHECK_SIZE = 10000;

/** Internal request within a bulk batch. */
interface BulkCheckRequest {
  id: string;
  subject: MelangeObject;
  relation: Relation;
  object: MelangeObject;
}

/** @internal Internals exposed by Checker for BulkCheckBuilder. */
export interface CheckerInternals {
  db: Queryable;
  cache: Cache;
  decision?: Decision;
  shouldValidate: boolean;
  databaseSchema: string;
  cacheKey(subject: MelangeObject, relation: Relation, object: MelangeObject): string;
}

/**
 * BulkCheckBuilder accumulates permission checks and executes them in a single
 * SQL call via check_permission_bulk.
 *
 * Use `checker.newBulkCheck()` to create one.
 *
 * @example
 * ```typescript
 * const results = await checker.newBulkCheck()
 *   .add(user, 'can_read', repo1)
 *   .add(user, 'can_read', repo2)
 *   .add(user, 'can_write', repo1)
 *   .execute();
 *
 * if (results.all()) {
 *   // All checks passed
 * }
 * ```
 */
export class BulkCheckBuilder {
  private readonly internals: CheckerInternals;
  private readonly requests: BulkCheckRequest[] = [];
  private readonly ids = new Set<string>();

  /** @internal Use `checker.newBulkCheck()` instead. */
  constructor(internals: CheckerInternals) {
    this.internals = internals;
  }

  /**
   * Add a permission check with an auto-generated ID (string index).
   * Returns the builder for chaining.
   */
  add(subject: MelangeObject, relation: Relation, object: MelangeObject): this {
    const id = String(this.requests.length);
    this.ids.add(id);
    this.requests.push({ id, subject, relation, object });
    return this;
  }

  /**
   * Add a permission check with a caller-supplied ID.
   * The ID must be non-empty and unique within the batch.
   *
   * @throws {Error} If id is empty or already used
   */
  addWithId(id: string, subject: MelangeObject, relation: Relation, object: MelangeObject): this {
    if (!id) {
      throw new Error('melange: BulkCheckBuilder.addWithId: id must not be empty');
    }
    if (this.ids.has(id)) {
      throw new Error(`melange: BulkCheckBuilder.addWithId: duplicate id "${id}"`);
    }
    this.ids.add(id);
    this.requests.push({ id, subject, relation, object });
    return this;
  }

  /**
   * Add checks for one subject+relation across multiple objects.
   * Each check gets an auto-generated ID.
   */
  addMany(subject: MelangeObject, relation: Relation, ...objects: MelangeObject[]): this {
    for (const obj of objects) {
      this.add(subject, relation, obj);
    }
    return this;
  }

  /**
   * Execute all accumulated checks in a single SQL call.
   * Results honour decision overrides, caching, and deduplication.
   */
  async execute(): Promise<BulkCheckResults> {
    const { db, cache, decision, shouldValidate, databaseSchema, cacheKey } = this.internals;

    // 1. Decision override -- return all-same results without DB call.
    if (decision) {
      return this.buildAllDecision(decision.allowed);
    }

    // 2. Empty batch.
    if (this.requests.length === 0) {
      return new BulkCheckResults([], new Map());
    }

    // 3. Batch size guard.
    if (this.requests.length > MAX_BULK_CHECK_SIZE) {
      throw new MelangeError(
        `bulk check size ${this.requests.length} exceeds maximum ${MAX_BULK_CHECK_SIZE}`,
      );
    }

    // 4. Validation.
    if (shouldValidate) {
      for (const r of this.requests) {
        validateObject(r.subject, 'subject');
        validateRelation(r.relation);
        validateObject(r.object, 'object');
      }
    }

    // 5. Deduplication -- map unique checks to original request indices.
    //    The cacheKey format doubles as the dedup key since both encode
    //    the same (subject, relation, object) triple.
    const dedup = new Map<string, number[]>();
    for (let i = 0; i < this.requests.length; i++) {
      const r = this.requests[i];
      const key = cacheKey(r.subject, r.relation, r.object);
      const existing = dedup.get(key);
      if (existing) {
        existing.push(i);
      } else {
        dedup.set(key, [i]);
      }
    }

    // 6. Cache lookup -- partition unique keys into cached and uncached.
    const outcomes = new Map<string, Decision>();
    const uncachedKeys: string[] = [];

    for (const [key, indices] of dedup) {
      const r = this.requests[indices[0]];
      const cached = await cache.get(cacheKey(r.subject, r.relation, r.object));
      if (cached !== undefined) {
        outcomes.set(key, cached);
      } else {
        uncachedKeys.push(key);
      }
    }

    // 7. SQL call for uncached checks.
    if (uncachedKeys.length > 0) {
      const subjectTypes: string[] = [];
      const subjectIds: string[] = [];
      const relations: string[] = [];
      const objectTypes: string[] = [];
      const objectIds: string[] = [];

      for (const key of uncachedKeys) {
        const r = this.requests[dedup.get(key)![0]];
        subjectTypes.push(r.subject.type);
        subjectIds.push(r.subject.id);
        relations.push(r.relation);
        objectTypes.push(r.object.type);
        objectIds.push(r.object.id);
      }

      const func = prefixIdent('check_permission_bulk', databaseSchema);

      const result = await db.query<{ idx: number; allowed: number }>(
        `SELECT idx, allowed FROM ${func}($1, $2, $3, $4, $5)`,
        [subjectTypes, subjectIds, relations, objectTypes, objectIds],
      );

      for (const row of result.rows) {
        // SQL idx is 1-based (WITH ORDINALITY), convert to 0-based.
        const zeroIdx = row.idx - 1;
        if (zeroIdx < 0 || zeroIdx >= uncachedKeys.length) {
          continue;
        }
        outcomes.set(uncachedKeys[zeroIdx], { allowed: row.allowed === 1 });
      }

      // 8. Cache store for DB results.
      for (const key of uncachedKeys) {
        const outcome = outcomes.get(key);
        if (outcome) {
          await cache.set(key, outcome);
        }
      }
    }

    // 9. Result assembly -- fan out deduplicated results to all original indices.
    const results: BulkCheckResult[] = new Array(this.requests.length);
    const byId = new Map<string, BulkCheckResult>();

    for (const [key, indices] of dedup) {
      const allowed = outcomes.get(key)?.allowed ?? false;

      for (const origIdx of indices) {
        const r = this.requests[origIdx];
        const result = new BulkCheckResult(r.id, origIdx, r.subject, r.relation, r.object, allowed);
        results[origIdx] = result;
        byId.set(r.id, result);
      }
    }

    return new BulkCheckResults(results, byId);
  }

  private buildAllDecision(allowed: boolean): BulkCheckResults {
    const results: BulkCheckResult[] = [];
    const byId = new Map<string, BulkCheckResult>();

    for (let i = 0; i < this.requests.length; i++) {
      const r = this.requests[i];
      const result = new BulkCheckResult(r.id, i, r.subject, r.relation, r.object, allowed);
      results.push(result);
      byId.set(r.id, result);
    }

    return new BulkCheckResults(results, byId);
  }
}

/**
 * BulkCheckResult holds the outcome of a single check within a bulk batch.
 */
export class BulkCheckResult {
  readonly id: string;
  readonly index: number;
  readonly subject: MelangeObject;
  readonly relation: Relation;
  readonly object: MelangeObject;
  readonly allowed: boolean;
  readonly error?: Error;

  /** @internal */
  constructor(
    id: string,
    index: number,
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    allowed: boolean,
    error?: Error,
  ) {
    this.id = id;
    this.index = index;
    this.subject = subject;
    this.relation = relation;
    this.object = object;
    this.allowed = allowed;
    this.error = error;
  }

  /** True if the permission was granted and no error occurred. */
  isAllowed(): boolean {
    return this.allowed && !this.error;
  }
}

/**
 * BulkCheckResults holds the outcomes of a bulk permission check.
 * Results are in the same order as the original requests.
 */
export class BulkCheckResults {
  private readonly _results: BulkCheckResult[];
  private readonly _byId: Map<string, BulkCheckResult>;

  /** @internal */
  constructor(results: BulkCheckResult[], byId: Map<string, BulkCheckResult>) {
    this._results = results;
    this._byId = byId;
  }

  /** Number of results. */
  get length(): number {
    return this._results.length;
  }

  /** Get result at the given index. Throws if out of range. */
  get(index: number): BulkCheckResult {
    if (index < 0 || index >= this._results.length) {
      throw new RangeError(`index ${index} out of range [0, ${this._results.length})`);
    }
    return this._results[index];
  }

  /** Get result by its ID, or undefined if not found. */
  getById(id: string): BulkCheckResult | undefined {
    return this._byId.get(id);
  }

  /** True if every check was allowed (false for empty batch). */
  all(): boolean {
    if (this._results.length === 0) return false;
    return this._results.every((r) => r.isAllowed());
  }

  /** True if at least one check was allowed. */
  any(): boolean {
    return this._results.some((r) => r.isAllowed());
  }

  /** True if no check was allowed (true for empty batch). */
  none(): boolean {
    return !this.any();
  }

  /** All results in request order. */
  results(): BulkCheckResult[] {
    return [...this._results];
  }

  /** Only the results where the check was allowed. */
  allowed(): BulkCheckResult[] {
    return this._results.filter((r) => r.isAllowed());
  }

  /** Only the results where the check was denied or errored. */
  denied(): BulkCheckResult[] {
    return this._results.filter((r) => !r.isAllowed());
  }

  /**
   * Returns null if all checks were allowed, or a BulkCheckDeniedError
   * describing the first denied check.
   *
   * For empty batches, returns null (no denials).
   */
  allOrError(): BulkCheckDeniedError | null {
    let first: BulkCheckResult | undefined;
    let deniedCount = 0;

    for (const r of this._results) {
      if (!r.isAllowed()) {
        if (!first) first = r;
        deniedCount++;
      }
    }

    if (!first) return null;

    return new BulkCheckDeniedError(first.subject, first.relation, first.object, first.index, deniedCount);
  }
}
