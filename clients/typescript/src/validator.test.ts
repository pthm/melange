/**
 * Unit tests for validators.
 */

import { describe, test, expect } from 'vitest';
import { validateObject, validateRelation } from './validator.js';
import type { MelangeObject } from './types.js';

describe('validateObject', () => {
  test('accepts valid object', () => {
    const obj: MelangeObject = { type: 'user', id: '123' };
    expect(() => validateObject(obj, 'subject')).not.toThrow();
  });

  test('accepts object with wildcard id', () => {
    const obj: MelangeObject = { type: 'user', id: '*' };
    expect(() => validateObject(obj, 'subject')).not.toThrow();
  });

  test('accepts object with userset relation', () => {
    const obj: MelangeObject = { type: 'group', id: '456', relation: 'member' };
    expect(() => validateObject(obj, 'subject')).not.toThrow();
  });

  test('rejects missing type', () => {
    const obj = { type: '', id: '123' } as MelangeObject;
    expect(() => validateObject(obj, 'subject')).toThrow(
      'subject.type is required'
    );
  });

  test('rejects non-string type', () => {
    const obj = { type: 123, id: '123' } as unknown as MelangeObject;
    expect(() => validateObject(obj, 'subject')).toThrow(
      'subject.type must be a string'
    );
  });

  test('rejects missing id', () => {
    const obj = { type: 'user', id: '' } as MelangeObject;
    expect(() => validateObject(obj, 'object')).toThrow(
      'object.id is required'
    );
  });

  test('rejects non-string id', () => {
    const obj = { type: 'user', id: 123 } as unknown as MelangeObject;
    expect(() => validateObject(obj, 'object')).toThrow(
      'object.id must be a string'
    );
  });

  test('accepts empty relation when provided', () => {
    const obj = { type: 'group', id: '456', relation: '' } as MelangeObject;
    // Validator doesn't check relation field on object - that's validated separately
    expect(() => validateObject(obj, 'subject')).not.toThrow();
  });

  test('accepts non-string relation when provided', () => {
    const obj = {
      type: 'group',
      id: '456',
      relation: 123,
    } as unknown as MelangeObject;
    // Validator doesn't check relation field on object - that's validated separately
    expect(() => validateObject(obj, 'subject')).not.toThrow();
  });

  test('rejects null object', () => {
    const obj = null as unknown as MelangeObject;
    expect(() => validateObject(obj, 'subject')).toThrow('subject is required');
  });

  test('rejects undefined object', () => {
    const obj = undefined as unknown as MelangeObject;
    expect(() => validateObject(obj, 'subject')).toThrow('subject is required');
  });

  test('custom field name appears in error message', () => {
    const obj = { type: '', id: '123' } as MelangeObject;
    expect(() => validateObject(obj, 'customField')).toThrow(
      'customField.type is required'
    );
  });
});

describe('validateRelation', () => {
  test('accepts valid relation', () => {
    expect(() => validateRelation('can_read')).not.toThrow();
  });

  test('accepts relation with underscores', () => {
    expect(() => validateRelation('can_read_write')).not.toThrow();
  });

  test('accepts relation with numbers', () => {
    expect(() => validateRelation('level_1_admin')).not.toThrow();
  });

  test('rejects empty relation', () => {
    expect(() => validateRelation('')).toThrow('relation is required');
  });

  test('rejects non-string relation', () => {
    expect(() => validateRelation(123 as unknown as string)).toThrow(
      'relation must be a string'
    );
  });

  test('rejects null relation', () => {
    expect(() => validateRelation(null as unknown as string)).toThrow(
      'relation is required'
    );
  });

  test('rejects undefined relation', () => {
    expect(() => validateRelation(undefined as unknown as string)).toThrow(
      'relation is required'
    );
  });

  test('rejects whitespace-only relation', () => {
    expect(() => validateRelation('   ')).toThrow(
      'relation cannot be empty'
    );
  });
});
