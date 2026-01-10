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
 */
export interface MelangeObject {
  readonly type: ObjectType;
  readonly id: string;
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
