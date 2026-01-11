# doctor

Health checks for melange authorization infrastructure.

## Responsibility

Validates that the authorization system is properly configured by checking:

- **Schema File** - Exists, parses correctly, no cyclic dependencies
- **Migration State** - Tracking table exists, schema is in sync
- **Generated Functions** - All expected functions present, no orphans
- **Tuples Source** - `melange_tuples` view exists with correct columns
- **Data Health** - Tuples reference valid types and relations

## Architecture Role

```
cmd/melange doctor
       │
       └── internal/doctor
               │
               ├── pkg/parser (schema parsing)
               ├── pkg/schema (validation)
               ├── pkg/migrator (migration state)
               └── internal/sqlgen (function analysis)
```

The doctor command provides diagnostic output for troubleshooting authorization issues.

## Output

Produces a `Report` with categorized check results:
- `StatusPass` - Check passed
- `StatusWarn` - Non-critical issue
- `StatusFail` - Critical issue that will cause failures

Each failing check includes a `FixHint` suggesting remediation steps.

## Key Checks

| Category | Validates |
|----------|-----------|
| Schema File | File exists, syntax valid, no cycles |
| Migration State | Tracking table, schema checksum match |
| Generated Functions | Dispatchers present, no missing/orphan functions |
| Tuples Source | View exists, required columns present |
| Data Health | Tuple types and relations match schema |
