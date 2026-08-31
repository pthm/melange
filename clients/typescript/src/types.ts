/**
 * Melange TypeScript Types
 *
 * This module provides type definitions for Melange authorization checks.
 * Types are designed to match the Go runtime for cross-language consistency.
 */

/**
 * ObjectType represents an authorization object type (e.g., "user", "repository").
 */
export type ObjectType = string;

/**
 * Relation represents an authorization relation (e.g., "owner", "can_read").
 */
export type Relation = string;

/**
 * Object represents an authorization object with type and ID.
 * Optionally includes a relation for userset references (e.g., "group#member").
 */
export interface MelangeObject {
  readonly type: ObjectType;
  readonly id: string;
  readonly relation?: Relation;
}

/**
 * Decision represents the result of a permission check.
 */
export interface Decision {
  readonly allowed: boolean;
}

/**
 * CheckRequest represents a permission check request.
 */
export interface CheckRequest {
  readonly subject: MelangeObject;
  readonly relation: Relation;
  readonly object: MelangeObject;
}

/**
 * ContextualTuple represents a tuple provided at request time.
 *
 * Contextual tuples are not persisted and only affect a single check/list call.
 * They're useful for temporary permissions or "what-if" scenarios.
 */
export interface ContextualTuple {
  readonly subject: MelangeObject;
  readonly relation: Relation;
  readonly object: MelangeObject;
}

/**
 * PageOptions configures pagination for list operations.
 */
export interface PageOptions {
  /**
   * Maximum number of results to return.
   * Zero or negative means no limit (returns all results).
   */
  limit?: number;

  /**
   * Cursor from a previous page.
   * If undefined, starts from the beginning.
   */
  after?: string;
}

/**
 * ObjectFilter narrows listObjects results to objects holding a direct relation
 * to a given subject.
 *
 * The relation must be directly assignable on the object type being listed. The
 * generated function knows that set and rejects anything else — including a
 * computed or tuple-to-userset relation, or a typo — rather than matching no
 * tuples and returning an empty list that reads like "no access".
 */
export interface ObjectFilter {
  /** A directly-assignable relation on the object type being listed. */
  readonly relation: Relation;

  /** The subject that relation must point at, e.g. { type: 'workspace', id: '7' }. */
  readonly subject: MelangeObject;
}

/**
 * ListObjectsOptions configures a listObjects call: pagination plus the
 * optional object filter.
 *
 * Filtering is applied before pagination, so limit and after count filtered
 * rows.
 */
export interface ListObjectsOptions extends PageOptions {
  /**
   * Narrow results to objects holding this relation to this subject.
   *
   * This is a Melange extension — OpenFGA's ListObjects has no object-side
   * filter.
   */
  filter?: ObjectFilter;
}

/**
 * ListResult contains paginated list results.
 */
export interface ListResult<T> {
  /** Items in this page */
  readonly items: T[];

  /** Cursor for the next page, if there are more results */
  readonly nextCursor?: string;
}
