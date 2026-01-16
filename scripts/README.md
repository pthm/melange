# Test Scripts

This directory contains scripts for running multi-language integration tests.

## Scripts

### `integration-test-runner.sh`

Runs all integration tests (Go + TypeScript) sequentially.

**Usage:**
```bash
# From project root
./scripts/integration-test-runner.sh

# Or via justfile
just test-integration-all
```

**How it works:**
- Runs Go integration tests (which start testcontainers)
- Runs TypeScript integration tests
- Prints summary of results
- Each test suite manages its own database via testcontainers

**With external database:**
```bash
export DATABASE_URL="postgresql://user:pass@host:port/dbname"
./scripts/integration-test-runner.sh
```

### `start-test-db.sh`

Starts a standalone PostgreSQL container for manual testing.

**Usage:**
```bash
# Start database and export URL
export DATABASE_URL=$(./scripts/start-test-db.sh)

# Run tests
just test-integration
just test-ts

# Stop database when done
docker stop melange-test-db
docker rm melange-test-db
```

**Features:**
- Creates a PostgreSQL 18 container named `melange-test-db`
- Reuses existing container if already running
- Prints `DATABASE_URL` for use in tests
- Container persists until manually stopped

## CI Integration

In CI environments, you can either:

1. **Use testcontainers** (default):
   ```bash
   just test-integration-all
   ```
   Each test suite gets its own container automatically.

2. **Use a shared database**:
   ```bash
   export DATABASE_URL=$(./scripts/start-test-db.sh)
   just test-integration
   just test-ts
   docker stop melange-test-db
   ```

## Example CI Workflow

```yaml
- name: Run integration tests
  run: |
    # Option 1: Let testcontainers handle everything
    just test-integration-all

    # Option 2: Use shared database
    export DATABASE_URL=$(./scripts/start-test-db.sh)
    just test-integration
    just test-ts
    docker stop melange-test-db
```

## Database Schema Setup

The Go test infrastructure automatically:
1. Starts a PostgreSQL testcontainer
2. Creates the template database with melange schema
3. Creates per-test databases from the template

TypeScript tests reuse this infrastructure when `DATABASE_URL` is set, or fall back to localhost if not.
