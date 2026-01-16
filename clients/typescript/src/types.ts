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
 * ListResult contains paginated list results.
 */
export interface ListResult<T> {
  /** Items in this page */
  readonly items: T[];

  /** Cursor for the next page, if there are more results */
  readonly nextCursor?: string;
}
