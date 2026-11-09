/**
 * Unit tests for database error mapping.
 *
 * A fake Queryable rejects every call with a driver-shaped error (pg and
 * postgres.js both carry the SQLSTATE as `code`), so no database is needed.
 */

import { describe, test, expect } from 'vitest';
import { Checker } from './checker.js';
import type { Queryable } from './database.js';
import { ErrorCodeValidation, MissingFunctionError, ValidationError } from './errors.js';

const notExposedMessage = 'relation "viewer" on type "document" is not exposed';

// pgError builds an error shaped like pg's DatabaseError.
function pgError(code: string, message: string): Error {
  return Object.assign(new Error(message), { code, severity: 'ERROR' });
}

function failingChecker(err: Error): Checker {
  const db: Queryable = {
    query: () => Promise.reject(err),
  };
  return new Checker(db);
}

const user = { type: 'user', id: 'alice' };
const doc = { type: 'document', id: '1' };

// Every Checker entry point that calls a public dispatcher.
const calls: Record<string, (c: Checker) => Promise<unknown>> = {
  check: (c) => c.check(user, 'viewer', doc),
  checkWithContextualTuples: (c) =>
    c.checkWithContextualTuples(user, 'viewer', doc, [{ subject: user, relation: 'viewer', object: doc }]),
  bulkCheck: (c) => c.newBulkCheck().add(user, 'viewer', doc).execute(),
  listObjects: (c) => c.listObjects(user, 'viewer', 'document'),
  listSubjects: (c) => c.listSubjects('user', 'viewer', doc),
  explain: (c) => c.explain(user, 'viewer', doc),
  expand: (c) => c.expand(doc, 'viewer'),
  expandRecursive: (c) => c.expandRecursive(doc, 'viewer'),
};

describe('database error mapping', () => {
  for (const [name, call] of Object.entries(calls)) {
    test(`${name} maps M2003 to ValidationError 2000`, async () => {
      const cause = pgError('M2003', notExposedMessage);
      const err = await call(failingChecker(cause)).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ValidationError);
      const ve = err as ValidationError;
      expect(ve.code).toBe(ErrorCodeValidation);
      expect(ve.code).toBe(2000);
      expect(ve.message).toBe(notExposedMessage);
      expect(ve.cause).toBe(cause);
    });
  }

  for (const name of ['expand', 'expandRecursive']) {
    test(`${name} maps a missing expand_permission to MissingFunctionError`, async () => {
      const cause = pgError('42883', 'function expand_permission(unknown, unknown, unknown, unknown, unknown) does not exist');
      const err = await calls[name](failingChecker(cause)).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(MissingFunctionError);
      expect((err as Error).message).toContain('expand_permission');
      expect((err as Error).cause).toBe(cause);
    });
  }

  test.each([
    ['check', 'check_permission'],
    ['listObjects', 'list_accessible_objects'],
    ['listSubjects', 'list_accessible_subjects'],
    ['explain', 'explain_permission'],
    ['bulkCheck', 'check_permission_bulk'],
  ])('%s maps a missing %s to MissingFunctionError', async (name, fn) => {
    const err = await calls[name](failingChecker(pgError('42883', `function ${fn}(unknown) does not exist`))).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(MissingFunctionError);
  });

  test('other errors pass through unchanged', async () => {
    const other = pgError('42883', 'function my_helper(integer) does not exist');
    expect(await calls.check(failingChecker(other)).catch((e: unknown) => e)).toBe(other);
    const plain = new Error('connection terminated');
    expect(await calls.explain(failingChecker(plain)).catch((e: unknown) => e)).toBe(plain);
  });

  test('input validation errors carry code 2000', () => {
    expect(new ValidationError('subject is required').code).toBe(ErrorCodeValidation);
  });
});
