/**
 * Database adapters for Melange.
 *
 * This module provides adapters for popular PostgreSQL clients.
 */

export { pgAdapter } from './pg.js';
export type { PgQueryable } from './pg.js';
export { postgresAdapter } from './postgres.js';
export type { PostgresSql } from './postgres.js';
