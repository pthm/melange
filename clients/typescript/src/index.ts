/**
 * @pthm/melange - TypeScript client for Melange PostgreSQL authorization
 *
 * Melange is an OpenFGA-compatible authorization library that runs entirely
 * in PostgreSQL. This TypeScript client provides type-safe access to the
 * authorization system.
 *
 * @packageDocumentation
 */

export { Checker, DecisionAllow, DecisionDeny } from './checker.js';
export type { CheckerOptions } from './checker.js';
export { Cache, NoopCache, MemoryCache } from './cache.js';
export { MelangeError, NotFoundError, ValidationError } from './errors.js';
export type { Queryable, QueryResult } from './database.js';
export { validateObject, validateRelation } from './validator.js';
export type {
  ObjectType,
  Relation,
  MelangeObject,
  Decision,
  CheckRequest,
  ContextualTuple,
  PageOptions,
  ListResult,
} from './types.js';

// Re-export adapters for convenience
export * from './adapters/index.js';
