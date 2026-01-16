# Changelog - TypeScript Client

## [Unreleased]

### Added
- Comprehensive test suite with Vitest
  - Unit tests for Cache implementations (NoopCache, MemoryCache)
  - Unit tests for input validators
  - Integration tests for Checker class
  - Integration tests for list operations with pagination
  - Tests for caching behavior and TTL
  - Tests for decision overrides
- Test setup infrastructure (`test/setup.ts`)
  - `createTestPool()` - Database connection helper
  - `verifyTestDatabase()` - Schema validation
  - Compatible with testcontainers and external databases
- Test documentation (`test/README.md`)
- Coverage reporting with `@vitest/coverage-v8`

### Changed
- Updated `package.json` with test scripts:
  - `test` - Run all tests
  - `test:watch` - Run tests in watch mode
  - `test:coverage` - Run tests with coverage report
- Added `.gitignore` for coverage and build artifacts

## [0.5.1] - 2025-01-15

### Added
- Initial TypeScript runtime implementation
- Checker class with permission checking
- List operations (listObjects, listSubjects) with pagination
- Caching support (MemoryCache with TTL)
- Input validation
- Database adapters for pg and postgres.js
- Error types (MelangeError, ValidationError, NotFoundError)
- Decision override support for testing
- Comprehensive README with examples
- TypeScript client code generator

### Implementation Status
- ✅ Permission checks (`check`)
- ✅ List operations (`listObjects`, `listSubjects`)
- ✅ Caching (memory-based with TTL)
- ✅ Input validation
- ✅ Database adapters
- ⚠️ Contextual tuples (API exists, backend support pending)
