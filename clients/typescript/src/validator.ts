/**
 * Input validation for Melange authorization operations.
 *
 * This module provides validation functions to ensure authorization
 * requests have valid structure before executing database queries.
 */

import type { MelangeObject, Relation } from './types.js';
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
