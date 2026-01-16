# TypeScript Tests

This directory contains tests for the TypeScript client library.

## Test Types

### Unit Tests (`src/**/*.test.ts`)
- **No database required**
- Test individual components in isolation
- Run with: `pnpm test -- --run src/`

Includes:
- `cache.test.ts` - Cache implementations (NoopCache, MemoryCache)
- `validator.test.ts` - Input validation functions

### Integration Tests (`test/**/*.test.ts`)
- **Requires PostgreSQL database** with melange schema
- Tests against real database
- Run with: `pnpm test`

Includes:
- `checker.integration.test.ts` - End-to-end Checker tests with real database

## Running Tests

### Quick Start (Unit Tests Only)
```bash
pnpm test -- --run src/
```

### Full Test Suite (with Database)

#### Option 1: Use Go Test Infrastructure
The TypeScript tests can use the same testcontainers database as Go tests:

```bash
# Terminal 1: Start Go tests (sets up database)
cd ../.. && just test-integration

# Terminal 2: Run TypeScript tests (uses same DATABASE_URL)
cd clients/typescript && pnpm test
```

#### Option 2: External Database
Set `DATABASE_URL` to point to a PostgreSQL database with melange schema:

```bash
export DATABASE_URL="postgresql://user:pass@localhost:5432/dbname"
pnpm test
```

#### Option 3: Using justfile
```bash
# Run TypeScript tests (from project root)
just test-ts

# Run in watch mode
just test-ts-watch

# Run with coverage
just test-ts-coverage
```

## Test Database Requirements

Integration tests require:
- PostgreSQL 14+ with melange schema installed
- `check_permission` function
- `list_objects` function
- `list_subjects` function
- `melange_tuples` view/table
- Domain tables: `users`, `organizations`, `repositories`, etc.

The test setup will automatically verify these exist before running.

## CI Integration

In CI, the Go test suite starts a testcontainers PostgreSQL instance and sets `DATABASE_URL` for all language clients to use. This ensures TypeScript tests run against the same database with the same schema.

## Coverage

Run with coverage report:
```bash
pnpm test:coverage
```

Coverage reports are generated in `coverage/` directory.
