/**
 * Error classes for Melange TypeScript client.
 *
 * This module provides structured error types for authorization operations,
 * making it easier to handle different failure modes.
 */

/**
 * MelangeError is the base error class for all melange errors.
 *
 * Applications can catch this to handle all melange-specific errors,
 * or catch specific subclasses for targeted error handling.
 */
export class MelangeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'MelangeError';
    // Restore prototype chain for instanceof checks
    Object.setPrototypeOf(this, MelangeError.prototype);
  }
}

/**
 * NotFoundError indicates a requested resource was not found.
 *
 * This is typically thrown when querying for objects or relations
 * that don't exist in the authorization model.
 */
export class NotFoundError extends MelangeError {
  constructor(message: string) {
    super(message);
    this.name = 'NotFoundError';
    Object.setPrototypeOf(this, NotFoundError.prototype);
  }
}

/**
 * ValidationError indicates invalid input to an authorization operation.
 *
 * This is thrown when required fields are missing, types are incorrect,
 * or values don't meet validation constraints.
 *
 * @example
 * ```typescript
 * try {
 *   await checker.check(null, 'can_read', repo);
 * } catch (err) {
 *   if (err instanceof ValidationError) {
 *     console.error('Invalid input:', err.message);
 *   }
 * }
 * ```
 */
export class ValidationError extends MelangeError {
  constructor(message: string) {
    super(message);
    this.name = 'ValidationError';
    Object.setPrototypeOf(this, ValidationError.prototype);
  }
}
