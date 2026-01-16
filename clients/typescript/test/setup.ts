/**
 * Test setup helper for integration tests.
 *
 * This module provides utilities to connect to the test database,
 * which can be either a testcontainers instance or an external database
 * specified via DATABASE_URL environment variable.
 */

import { Pool, type PoolConfig } from 'pg';

/**
 * GetDatabaseURL returns the database connection string for tests.
 *
 * Priority order:
 * 1. DATABASE_URL environment variable (for CI or external database)
 * 2. Falls back to default local PostgreSQL for manual testing
 *
 * In CI, the Go test suite starts a testcontainers PostgreSQL instance
 * and sets DATABASE_URL for all language clients to use.
 */
export function getDatabaseURL(): string {
  const url = process.env.DATABASE_URL;
  if (url) {
    return url;
  }

  // Fallback for local development (assumes local PostgreSQL)
  return 'postgresql://test:test@localhost:5432/postgres';
}

/**
 * Creates a PostgreSQL connection pool for testing.
 *
 * @returns A pg Pool instance connected to the test database
 */
export function createTestPool(): Pool {
  const connectionString = getDatabaseURL();

  const config: PoolConfig = {
    connectionString,
    max: 10,
    idleTimeoutMillis: 30000,
    connectionTimeoutMillis: 5000,
  };

  return new Pool(config);
}

/**
 * Verifies the test database has required melange schema.
 *
 * Checks:
 * - check_permission function exists
 * - list_objects function exists
 * - list_subjects function exists
 * - melange_tuples view/table exists
 *
 * @param pool - Database connection pool
 * @throws Error if required schema is missing
 */
export async function verifyTestDatabase(pool: Pool): Promise<void> {
  // Check for check_permission function
  const checkPermissionExists = await pool.query(`
    SELECT EXISTS (
      SELECT 1 FROM pg_proc p
      JOIN pg_namespace n ON p.pronamespace = n.oid
      WHERE p.proname = 'check_permission'
      AND n.nspname = current_schema()
    ) as exists
  `);

  if (!checkPermissionExists.rows[0].exists) {
    throw new Error('check_permission function not found - database may not have melange schema installed');
  }

  // Check for list_accessible_objects function
  const listObjectsExists = await pool.query(`
    SELECT EXISTS (
      SELECT 1 FROM pg_proc p
      JOIN pg_namespace n ON p.pronamespace = n.oid
      WHERE p.proname = 'list_accessible_objects'
      AND n.nspname = current_schema()
    ) as exists
  `);

  if (!listObjectsExists.rows[0].exists) {
    throw new Error('list_accessible_objects function not found - database may not have melange schema installed');
  }

  // Check for list_accessible_subjects function
  const listSubjectsExists = await pool.query(`
    SELECT EXISTS (
      SELECT 1 FROM pg_proc p
      JOIN pg_namespace n ON p.pronamespace = n.oid
      WHERE p.proname = 'list_accessible_subjects'
      AND n.nspname = current_schema()
    ) as exists
  `);

  if (!listSubjectsExists.rows[0].exists) {
    throw new Error('list_accessible_subjects function not found - database may not have melange schema installed');
  }

  // Check for melange_tuples view/table
  const tuplesExists = await pool.query(`
    SELECT EXISTS (
      SELECT 1 FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      WHERE c.relname = 'melange_tuples'
      AND n.nspname = current_schema()
      AND c.relkind IN ('r', 'v', 'm')
    ) as exists
  `);

  if (!tuplesExists.rows[0].exists) {
    throw new Error('melange_tuples not found - database may not have melange schema installed');
  }
}

/**
 * Cleanup helper to close pool and release connections.
 *
 * @param pool - Database connection pool to close
 */
export async function closeTestPool(pool: Pool): Promise<void> {
  await pool.end();
}
