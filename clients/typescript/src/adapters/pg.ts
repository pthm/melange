/**
 * node-postgres (pg) adapter for Melange.
 *
 * This module provides an adapter to use node-postgres Pool or Client
 * with the Melange Checker.
 */

import type { Queryable, QueryResult } from '../database.js';

/**
 * PgQueryable represents a node-postgres client that can execute queries.
 *
 * This interface matches the query signature of both Pool and Client from 'pg'.
 */
export interface PgQueryable {
  query<T = any>(text: string, params?: any[]): Promise<{ rows: T[] }>;
}

/**
 * pgAdapter wraps a node-postgres Pool or Client for use with Checker.
 *
 * Note: In most cases, you don't need this adapter. The pg Pool and Client
 * already implement the Queryable interface and can be used directly.
 *
 * This adapter is provided for explicit type conversion if needed.
 *
 * @param client - A pg Pool or Client
 * @returns A Queryable instance
 *
 * @example
 * ```typescript
 * import { Pool } from 'pg';
 * import { Checker, pgAdapter } from '@pthm/melange';
 *
 * const pool = new Pool({ connectionString: process.env.DATABASE_URL });
 * const checker = new Checker(pgAdapter(pool));
 *
 * // Or use the pool directly (preferred):
 * const checker = new Checker(pool);
 * ```
 */
export function pgAdapter(client: PgQueryable): Queryable {
  return {
    async query<T = any>(text: string, params?: any[]): Promise<QueryResult<T>> {
      const result = await client.query<T>(text, params);
      return { rows: result.rows };
    },
  };
}
