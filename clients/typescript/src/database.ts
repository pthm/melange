/**
 * Database abstraction for Melange TypeScript client.
 *
 * This module defines the minimal interface for database query execution,
 * allowing the Checker to work with any PostgreSQL client library.
 */

/**
 * QueryResult represents the result of a database query.
 */
export interface QueryResult<T = any> {
  readonly rows: T[];
}

/**
 * Queryable defines the minimal interface for database query execution.
 *
 * This interface is compatible with popular PostgreSQL clients:
 * - pg (node-postgres): client.query(text, params)
 * - postgres.js: sql.unsafe(text, params)
 * - kysely: db.executeQuery({ sql, parameters })
 *
 * Applications can use adapter functions to convert their database client
 * to the Queryable interface.
 *
 * @example
 * ```typescript
 * import { Pool } from 'pg';
 * import { Checker } from '@pthm/melange';
 *
 * const pool = new Pool({ connectionString: process.env.DATABASE_URL });
 *
 * // pg's Pool already implements Queryable
 * const checker = new Checker(pool);
 * ```
 */
export interface Queryable {
  /**
   * Execute a SQL query and return results.
   *
   * @param text - SQL query text
   * @param params - Query parameters (optional)
   * @returns Promise resolving to query results
   */
  query<T = any>(text: string, params?: any[]): Promise<QueryResult<T>>;
}
