/**
 * Error classes for Melange TypeScript client.
 *
 * This module provides structured error types for authorization operations,
 * making it easier to handle different failure modes.
 */

import type { MelangeObject, Relation } from './types.js';

/**
 * MelangeError is the base error class for all melange errors.
 *
 * Applications can catch this to handle all melange-specific errors,
 * or catch specific subclasses for targeted error handling.
 */
export class MelangeError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
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
 * OpenFGA error code for an invalid request (bad relation, type, etc.).
 */
export const ErrorCodeValidation = 2000;

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
  /**
   * The OpenFGA-compatible error code: {@link ErrorCodeValidation} (2000)
   * for invalid input, including a relation the database does not expose.
   */
  readonly code: number;

  constructor(message: string, code: number = ErrorCodeValidation, options?: ErrorOptions) {
    super(message, options);
    this.name = 'ValidationError';
    this.code = code;
    Object.setPrototypeOf(this, ValidationError.prototype);
  }
}

/**
 * MissingFunctionError indicates that a melange SQL function does not exist
 * in the database (SQLSTATE 42883): the database was not migrated, was
 * migrated into another schema, or was migrated by a melange release without
 * the function (such as `expand_permission`). Run `melange migrate`.
 *
 * A family turned off in `sql.functions` is not this error: its public
 * function is still installed and raises M2003, thrown as a
 * {@link ValidationError}.
 */
export class MissingFunctionError extends MelangeError {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = 'MissingFunctionError';
    Object.setPrototypeOf(this, MissingFunctionError.prototype);
  }
}

// PostgreSQL error codes mapped by mapDatabaseError.
const pgUndefinedFunction = '42883';
const pgNotExposed = 'M2003'; // relation not exposed by the SQL API

// Dispatcher names whose absence means melange is not (fully) installed.
const melangeFunctions = [
  'check_permission',
  'list_accessible',
  'explain_permission',
  'expand_permission',
];

/**
 * mapDatabaseError maps a database driver error to a melange error, the
 * way the Go runtime's Checker does:
 *
 * - SQLSTATE `M2003` (a public dispatcher was asked for a relation the model
 *   defines but the SQL API does not expose) becomes a {@link ValidationError}
 *   with code 2000 and the database message, which names the relation and
 *   the type.
 * - SQLSTATE `42883` naming a melange dispatcher becomes a
 *   {@link MissingFunctionError}.
 *
 * Any other error is returned unchanged. Drivers that report the SQLSTATE
 * as `code` (pg, postgres.js) are supported.
 */
export function mapDatabaseError(err: unknown): unknown {
  if (typeof err !== 'object' || err === null) {
    return err;
  }
  const { code, message } = err as { code?: unknown; message?: unknown };
  const text = typeof message === 'string' ? message : String(err);
  if (code === pgNotExposed) {
    return new ValidationError(text, ErrorCodeValidation, { cause: err });
  }
  if (code === pgUndefinedFunction && melangeFunctions.some((fn) => text.includes(fn))) {
    return new MissingFunctionError(`melange: authorization function missing: ${text}`, { cause: err });
  }
  return err;
}

/**
 * BulkCheckDeniedError is thrown by BulkCheckResults.allOrError() when at least
 * one check in the batch was denied. It carries the details of the first denied check.
 */
export class BulkCheckDeniedError extends MelangeError {
  readonly subject: MelangeObject;
  readonly relation: Relation;
  readonly object: MelangeObject;
  readonly index: number;
  readonly total: number;

  constructor(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    index: number,
    total: number,
  ) {
    super(
      `permission denied: ${subject.type}:${subject.id} ${relation} ${object.type}:${object.id} (${total} checks denied, first at index ${index})`,
    );
    this.name = 'BulkCheckDeniedError';
    this.subject = subject;
    this.relation = relation;
    this.object = object;
    this.index = index;
    this.total = total;
    Object.setPrototypeOf(this, BulkCheckDeniedError.prototype);
  }
}

/**
 * Type guard for BulkCheckDeniedError.
 */
export function isBulkCheckDeniedError(err: unknown): err is BulkCheckDeniedError {
  return err instanceof BulkCheckDeniedError;
}
