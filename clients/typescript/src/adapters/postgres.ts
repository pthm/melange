/**
 * postgres.js adapter for Melange.
 *
 * This module provides an adapter to use postgres.js with the Melange Checker.
 */

import type { Queryable, QueryResult } from '../database.js';

/**
 * PostgresSql represents a postgres.js Sql instance.
 *
 * This is a minimal interface matching postgres.js's unsafe method.
 */
export interface PostgresSql {
  unsafe<T = any>(text: string, params?: any[]): Promise<T[]>;
}

/**
 * postgresAdapter wraps a postgres.js Sql instance for use with Checker.
 *
 * @param sql - A postgres.js Sql instance
 * @returns A Queryable instance
 *
 * @example
 * ```typescript
 * import postgres from 'postgres';
 * import { Checker, postgresAdapter } from '@pthm/melange';
 *
 * const sql = postgres(process.env.DATABASE_URL);
 * const checker = new Checker(postgresAdapter(sql));
 *
 * const decision = await checker.check(
 *   { type: 'user', id: '123' },
 *   'can_read',
 *   { type: 'repository', id: '456' }
 * );
 * ```
 */
export function postgresAdapter(sql: PostgresSql): Queryable {
  return {
    async query<T = any>(text: string, params: any[] = []): Promise<QueryResult<T>> {
      const rows = await sql.unsafe<T>(text, params);
      return { rows };
    },
  };
}
