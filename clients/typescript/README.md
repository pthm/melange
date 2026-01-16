# @pthm/melange

TypeScript client for Melange PostgreSQL authorization.

Melange is an OpenFGA-compatible authorization library that runs entirely in PostgreSQL. This TypeScript client provides type-safe access to the authorization system.

## Installation

```bash
npm install @pthm/melange pg
# or
yarn add @pthm/melange pg
# or
pnpm add @pthm/melange pg
```

## Quick Start

```typescript
import { Checker } from '@pthm/melange';
import { Pool } from 'pg';

// Create a PostgreSQL connection pool
const pool = new Pool({
  connectionString: process.env.DATABASE_URL,
});

// Create a checker instance
const checker = new Checker(pool);

// Perform a permission check
const decision = await checker.check(
  { type: 'user', id: '123' },
  'can_read',
  { type: 'repository', id: '456' }
);

if (decision.allowed) {
  console.log('Access granted!');
} else {
  console.log('Access denied.');
}
```

## Features

### Permission Checks

```typescript
// Check if a user can read a repository
const decision = await checker.check(
  { type: 'user', id: '123' },
  'can_read',
  { type: 'repository', id: '456' }
);
```

### List Operations

```typescript
// List all repositories a user can read
const result = await checker.listObjects(
  { type: 'user', id: '123' },
  'can_read',
  'repository',
  { limit: 100 }
);

for (const repoId of result.items) {
  console.log(`Repository: ${repoId}`);
}

// List all users who can read a repository
const users = await checker.listSubjects(
  'user',
  'can_read',
  { type: 'repository', id: '456' },
  { limit: 100 }
);
```

### Caching

```typescript
import { Checker, MemoryCache } from '@pthm/melange';

// Create a checker with caching
const cache = new MemoryCache(60000); // 60 second TTL
const checker = new Checker(pool, { cache });

// First check hits the database
await checker.check(user, 'can_read', repo);

// Second check within 60s uses the cache
await checker.check(user, 'can_read', repo); // cached
```

### Decision Overrides for Testing

```typescript
import { Checker, DecisionAllow, DecisionDeny } from '@pthm/melange';

// Test authorized paths
const allowChecker = new Checker(pool, { decision: DecisionAllow });
await allowChecker.check(user, 'can_read', repo); // always returns { allowed: true }

// Test unauthorized paths
const denyChecker = new Checker(pool, { decision: DecisionDeny });
await denyChecker.check(user, 'can_read', repo); // always returns { allowed: false }
```

### Contextual Tuples

```typescript
// Check with temporary permissions
const decision = await checker.checkWithContextualTuples(
  { type: 'user', id: '123' },
  'can_read',
  { type: 'document', id: '789' },
  [
    {
      subject: { type: 'user', id: '123' },
      relation: 'temp_access',
      object: { type: 'document', id: '789' }
    }
  ]
);
```

## Database Adapters

The runtime works with any PostgreSQL client that implements the `Queryable` interface:

```typescript
interface Queryable {
  query<T>(text: string, params?: any[]): Promise<{ rows: T[] }>;
}
```

### node-postgres (pg)

node-postgres Pool and Client already implement `Queryable` and can be used directly:

```typescript
import { Checker } from '@pthm/melange';
import { Pool } from 'pg';

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const checker = new Checker(pool); // Works directly
```

### postgres.js

Use the `postgresAdapter` to wrap a postgres.js instance:

```typescript
import postgres from 'postgres';
import { Checker, postgresAdapter } from '@pthm/melange';

const sql = postgres(process.env.DATABASE_URL);
const checker = new Checker(postgresAdapter(sql));
```

## Generated Client Code

Generate type-safe constants and factory functions from your schema:

```bash
melange generate client --runtime typescript --schema schema.fga --output ./src/authz/
```

This generates three files:

### types.ts

```typescript
export const ObjectTypes = {
  User: "user",
  Repository: "repository",
} as const;

export type ObjectType = (typeof ObjectTypes)[keyof typeof ObjectTypes];

export const Relations = {
  CanRead: "can_read",
  Owner: "owner",
} as const;

export type Relation = (typeof Relations)[keyof typeof Relations];
```

### schema.ts

```typescript
import type { MelangeObject } from '@pthm/melange';
import { ObjectTypes } from './types.js';

export function user(id: string): MelangeObject {
  return { type: ObjectTypes.User, id };
}

export function repository(id: string): MelangeObject {
  return { type: ObjectTypes.Repository, id };
}

export function anyUser(): MelangeObject {
  return { type: ObjectTypes.User, id: '*' };
}
```

### Usage

```typescript
import { Checker } from '@pthm/melange';
import { user, repository, Relations } from './authz/index.js';

const decision = await checker.check(
  user('123'),
  Relations.CanRead,
  repository('456')
);
```

## API Reference

### Checker

```typescript
class Checker {
  constructor(db: Queryable, options?: CheckerOptions);

  check(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    contextualTuples?: ContextualTuple[]
  ): Promise<Decision>;

  listObjects(
    subject: MelangeObject,
    relation: Relation,
    objectType: ObjectType,
    options?: PageOptions
  ): Promise<ListResult<string>>;

  listSubjects(
    subjectType: ObjectType,
    relation: Relation,
    object: MelangeObject,
    options?: PageOptions
  ): Promise<ListResult<string>>;

  checkWithContextualTuples(
    subject: MelangeObject,
    relation: Relation,
    object: MelangeObject,
    contextualTuples: ContextualTuple[]
  ): Promise<Decision>;
}
```

### CheckerOptions

```typescript
interface CheckerOptions {
  cache?: Cache;                // Default: NoopCache
  decision?: Decision;          // For testing only
  validateRequest?: boolean;    // Default: true
  validateUserset?: boolean;    // Default: true
}
```

### Cache

```typescript
interface Cache {
  get(key: string): Promise<Decision | undefined>;
  set(key: string, value: Decision): Promise<void>;
  clear(): Promise<void>;
}

class NoopCache implements Cache { }  // No caching
class MemoryCache implements Cache {  // In-memory with TTL
  constructor(ttlMs?: number);
}
```

## Error Handling

```typescript
import { MelangeError, ValidationError, NotFoundError } from '@pthm/melange';

try {
  await checker.check(user, 'can_read', repo);
} catch (err) {
  if (err instanceof ValidationError) {
    console.error('Invalid input:', err.message);
  } else if (err instanceof NotFoundError) {
    console.error('Resource not found:', err.message);
  } else if (err instanceof MelangeError) {
    console.error('Melange error:', err.message);
  } else {
    console.error('Unknown error:', err);
  }
}
```

## Requirements

- Node.js 18 or higher
- PostgreSQL 14 or higher
- Melange schema and functions installed in your database

## License

MIT

## Contributing

See the [main repository](https://github.com/pthm/melange) for contribution guidelines.
