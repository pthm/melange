/**
 * Input validation for Melange authorization operations.
 *
 * This module provides validation functions to ensure authorization
 * requests have valid structure before executing database queries.
 */

import type { MelangeObject, Relation, ObjectFilter } from './types.js';
import { ValidationError } from './errors.js';

/**
 * Validate an object for permission checks.
 *
 * Ensures the object has required fields (type and id) and they are
 * non-empty strings.
 *
 * @param obj - The object to validate
 * @param name - Parameter name for error messages (e.g., "subject", "object")
 * @throws {ValidationError} If validation fails
 */
export function validateObject(obj: MelangeObject, name: string): void {
  if (!obj) {
    throw new ValidationError(`${name} is required`);
  }
  if (!obj.type) {
    throw new ValidationError(`${name}.type is required`);
  }
  if (!obj.id) {
    throw new ValidationError(`${name}.id is required`);
  }
  if (typeof obj.type !== 'string') {
    throw new ValidationError(`${name}.type must be a string`);
  }
  if (typeof obj.id !== 'string') {
    throw new ValidationError(`${name}.id must be a string`);
  }
  if (obj.type.trim() === '') {
    throw new ValidationError(`${name}.type cannot be empty`);
  }
  if (obj.id.trim() === '') {
    throw new ValidationError(`${name}.id cannot be empty`);
  }
}

/**
 * Validate a relation.
 *
 * Ensures the relation is a non-empty string.
 *
 * @param relation - The relation to validate
 * @throws {ValidationError} If validation fails
 */
export function validateRelation(relation: Relation): void {
  if (!relation) {
    throw new ValidationError('relation is required');
  }
  if (typeof relation !== 'string') {
    throw new ValidationError('relation must be a string');
  }
  if (relation.trim() === '') {
    throw new ValidationError('relation cannot be empty');
  }
}

/**
 * Build the wire form of an object filter: `relation@subject_type:subject_id`.
 *
 * The generated SQL function parses this single parameter back into its three
 * parts, so a delimiter appearing inside a part would reach the database as a
 * filter naming a different relation or subject than the caller asked for.
 * Rejecting them here turns that into a client-side error instead of a silently
 * mis-scoped result.
 *
 * Returns null for an absent filter, which binds as SQL NULL and disables
 * filtering entirely.
 *
 * @param filter - The filter to encode, or undefined for no filter
 * @returns The encoded filter string, or null
 * @throws {ValidationError} If the filter is malformed
 */
export function buildObjectFilter(filter?: ObjectFilter): string | null {
  if (!filter) {
    return null;
  }
  validateRelation(filter.relation);
  validateObject(filter.subject, 'filter.subject');

  if (/[@:#]/.test(filter.relation)) {
    throw new ValidationError(
      `filter.relation ${JSON.stringify(filter.relation)} cannot contain '@', ':' or '#'`
    );
  }
  if (/[@#]/.test(filter.subject.type)) {
    throw new ValidationError(
      `filter.subject.type ${JSON.stringify(filter.subject.type)} cannot contain '@' or '#'`
    );
  }
  // A userset subject would need a filtered expansion to resolve; only direct
  // relations are filterable.
  if (filter.subject.id.includes('#')) {
    throw new ValidationError(
      `filter.subject.id ${JSON.stringify(filter.subject.id)} names a userset; only direct relations are filterable`
    );
  }
  return `${filter.relation}@${filter.subject.type}:${filter.subject.id}`;
}
