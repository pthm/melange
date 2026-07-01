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
export { MelangeError, NotFoundError, ValidationError, BulkCheckDeniedError, isBulkCheckDeniedError } from './errors.js';
export { BulkCheckBuilder, BulkCheckResult, BulkCheckResults, MAX_BULK_CHECK_SIZE } from './bulk-check.js';
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
export type {
  NodeType,
  TupleRef,
  SubjectRef,
  TraceNode,
  Trace,
  ExplainOptions,
} from './trace.js';
export type {
  UsersetTree,
  UsersetTreeNode,
  Leaf,
  Users,
  Computed,
  TupleToUserset,
  Difference,
  Nodes,
  ExpandOptions,
} from './expand.js';
export { flattenUsers } from './expand.js';

// Re-export adapters for convenience
export * from './adapters/index.js';
