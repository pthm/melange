# Testing Guide

This document describes the testing infrastructure for Melange.

## Test Types

### Unit Tests
Fast tests that don't require database access.

**Go:**
```bash
just test-unit
# or
go test -short ./...
```

**TypeScript:**
```bash
cd clients/typescript
pnpm test -- --run src/
```

### Integration Tests
Tests that require a PostgreSQL database with melange schema.

**Go only:**
```bash
just test-integration
```

**TypeScript only:**
```bash
just test-ts
```

**All languages:**
```bash
just test-integration-all
```

### OpenFGA Compatibility Tests
Comprehensive tests against the OpenFGA test suite.

```bash
just test-openfga                    # All tests
just test-openfga-feature Exclusion  # Single category
just test-openfga-name <test_name>   # Single test
```

## Database Setup

Integration tests use one of two approaches:

### 1. Testcontainers (Default)
Tests automatically start a PostgreSQL container via testcontainers.

**Requirements:**
- Docker installed and running
- No additional configuration needed

**How it works:**
- Go tests start a singleton PostgreSQL 18 container
- Schema and test data are installed automatically
- Container is cleaned up after tests complete

### 2. External Database
Point tests to an existing PostgreSQL database.

```bash
export DATABASE_URL="postgresql://user:pass@host:port/dbname"
just test-integration
just test-ts
```

## Multi-Language Testing

The project includes test infrastructure for multiple language clients (Go, TypeScript).

### Running All Integration Tests

```bash
# Run Go and TypeScript tests together
just test-integration-all
```

This script:
1. Runs Go integration tests (starts testcontainers)
2. Runs TypeScript integration tests
3. Reports results for both

### Manual Database Control

For development, you can start a persistent test database:

```bash
# Start database
export DATABASE_URL=$(./scripts/start-test-db.sh)

# Run tests as many times as needed
just test-integration
just test-ts

# Stop database when done
docker stop melange-test-db
docker rm melange-test-db
```

## TypeScript-Specific Testing

### Unit Tests Only (No Database)
```bash
cd clients/typescript
pnpm test -- --run src/
```

Runs:
- `cache.test.ts` - Cache implementations
- `validator.test.ts` - Input validation

### Integration Tests
```bash
cd clients/typescript
pnpm test
```

Runs tests against a real PostgreSQL database:
- Permission checks
- List operations
- Caching behavior
- Validation

### Watch Mode
```bash
cd clients/typescript
pnpm test:watch
```

### Coverage Report
```bash
cd clients/typescript
pnpm test:coverage
```

Coverage reports are generated in `clients/typescript/coverage/`.

## CI/CD

GitHub Actions runs all tests automatically:

- **Lint**: Code quality checks
- **Unit Tests**: Go (short mode, no database)
- **Integration Tests**: Go + TypeScript (with testcontainers)
- **OpenFGA Tests**: Compatibility test suite

See `.github/workflows/ci.yml` for details.

## Test Database Schema

Integration tests use a test schema defined in `test/testutil/testdata/`:

- `schema.fga` - OpenFGA authorization model
- `domain_tables.sql` - Domain tables (users, organizations, repositories, etc.)
- `tuples_view.sql` - Melange tuples view over domain tables

The Go test infrastructure automatically:
1. Creates a template database with schema
2. Creates per-test databases from template for isolation
3. Runs migrations and installs melange functions

TypeScript tests reuse this infrastructure.

## Benchmarks

Run performance benchmarks:

```bash
just bench                           # All benchmarks
just bench-openfga                   # OpenFGA compatibility benchmarks
just bench-openfga-category DirectAssignment
```

## Test Helpers

### Go Test Utilities
`test/testutil/` provides:
- `DB(t)` - Get a database connection for tests
- `Fixtures` - Pre-populated test data
- `BulkFixtures` - Large-scale data for performance testing

### TypeScript Test Utilities
`clients/typescript/test/setup.ts` provides:
- `createTestPool()` - Create database connection pool
- `verifyTestDatabase()` - Verify melange schema is installed
- `closeTestPool()` - Clean up connections

## Debugging Tests

### Enable Verbose Output

**Go:**
```bash
go test -v ./...
```

**TypeScript:**
```bash
pnpm test -- --reporter=verbose
```

### Run Single Test

**Go:**
```bash
go test -run TestName ./...
```

**TypeScript:**
```bash
pnpm test -- --grep "test name"
```

### Database Logs

When using testcontainers, view container logs:
```bash
docker ps  # Find container ID
docker logs <container-id>
```

When using manual database:
```bash
docker logs melange-test-db
```

## Troubleshooting

### "password authentication failed for user test"
The TypeScript tests can't connect to the database. Either:
- Set `DATABASE_URL` to point to a valid database
- Run Go tests first (which start testcontainers)
- Use `./scripts/start-test-db.sh` to start a test database

### "testcontainers: failed to start container"
Docker is not running or not accessible. Ensure:
- Docker Desktop is running
- Your user has permission to access Docker
- `/var/run/docker.sock` is accessible

### "relation melange_tuples does not exist"
The database doesn't have the melange schema. Either:
- Let testcontainers handle it (default)
- Run migrations on your external database: `just migrate`

## Contributing

When adding tests:

1. **Add unit tests** for pure logic (no database)
2. **Add integration tests** for database interactions
3. **Follow existing patterns** in test files
4. **Use test helpers** from `testutil/` or `test/setup.ts`
5. **Keep tests fast** - use fixtures instead of creating data
6. **Ensure tests are isolated** - don't depend on other tests

See the main contributing guide for more details.
