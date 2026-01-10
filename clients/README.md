# Melange Language Clients

This directory contains language-specific runtime clients for Melange.

## Available Clients

| Language   | Directory     | Package          | Status           |
|------------|---------------|------------------|------------------|
| Go         | `../melange/` | `melange`        | Implemented      |
| TypeScript | `typescript/` | `@pthm/melange`  | Placeholder      |
| Python     | `python/`     | `melange`        | Placeholder      |

## Go Client

The Go runtime is the primary implementation and lives at the repository root
in `melange/` for optimal import ergonomics:

```go
import "github.com/pthm/melange/melange"

checker := melange.NewChecker(db)
decision, err := checker.Check(ctx, subject, relation, object)
```

## Future Clients

### TypeScript

The TypeScript client will provide:
- Full async/await support
- Works with any PostgreSQL driver (pg, postgres.js)
- Type-safe generated code from schemas

```typescript
import { Checker } from '@pthm/melange';
import { Pool } from 'pg';

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const checker = new Checker(pool);

const decision = await checker.check(user('123'), 'can_read', repository('456'));
```

### Python

The Python client will provide:
- Async support with asyncpg and psycopg
- Type hints throughout
- Generated dataclasses from schemas

```python
from melange import Checker
import asyncpg

pool = await asyncpg.create_pool(dsn="postgresql://...")
checker = Checker(pool)

decision = await checker.check(user("123"), "can_read", repository("456"))
```

## Generated Client Code

All languages support generating type-safe client code from schemas:

```bash
# Go
melange generate client --runtime go --schema schema.fga --output ./authz/

# TypeScript (when implemented)
melange generate client --runtime typescript --schema schema.fga --output ./src/authz/

# Python (when implemented)
melange generate client --runtime python --schema schema.fga --output ./authz/
```

Generated code includes:
- Object type constants
- Relation constants
- Factory functions for creating objects

## Contributing

To add a new language client:

1. Create the runtime directory: `clients/<language>/`
2. Implement the runtime with Checker, Cache, and types
3. Create a generator in `internal/clientgen/<language>/`
4. Register the generator in `pkg/clientgen/api.go`
5. Add tests for both runtime and code generation

See the Go implementation as a reference:
- Runtime: `melange/`
- Generator: `internal/clientgen/go/`
