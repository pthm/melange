/**
 * @pthm/melange - TypeScript client for Melange PostgreSQL authorization
 *
 * Melange is an OpenFGA-compatible authorization library that runs entirely
 * in PostgreSQL. This TypeScript client provides type-safe access to the
 * authorization system.
 *
 * @packageDocumentation
 */

export { Checker } from './checker.js';
export type {
  ObjectType,
  Relation,
  MelangeObject,
  Decision,
  CheckRequest,
} from './types.js';
