/**
 * Melange TypeScript Checker
 *
 * This module will provide the Checker class for performing authorization checks.
 * Currently a placeholder - implementation coming in a future release.
 */

import type { MelangeObject, Relation, Decision } from './types.js';

/**
 * Checker performs authorization checks against a PostgreSQL database.
 *
 * @example
 * ```typescript
 * import { Checker } from '@pthm/melange';
 * import { Pool } from 'pg';
 *
 * const pool = new Pool({ connectionString: process.env.DATABASE_URL });
 * const checker = new Checker(pool);
 *
 * const decision = await checker.check(
 *   { type: 'user', id: '123' },
 *   'can_read',
 *   { type: 'repository', id: '456' }
 * );
 * ```
 */
export class Checker {
  /**
   * Creates a new Checker instance.
   *
   * @param db - A PostgreSQL client or pool with query capability
   */
  constructor(_db: unknown) {
    throw new Error(
      'Melange TypeScript runtime is not yet implemented. ' +
      'Use the Go runtime (github.com/pthm/melange/melange) for now.'
    );
  }

  /**
   * Check performs a permission check.
   *
   * @param subject - The subject requesting access
   * @param relation - The relation to check
   * @param object - The object being accessed
   * @returns A Decision indicating whether access is allowed
   */
  async check(
    _subject: MelangeObject,
    _relation: Relation,
    _object: MelangeObject
  ): Promise<Decision> {
    throw new Error('Not implemented');
  }
}
