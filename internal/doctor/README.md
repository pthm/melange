# doctor

Health checks for melange authorization infrastructure.

## Responsibility

Validates that the authorization system is properly configured by checking:

- **Schema File** - Exists, parses correctly, no cyclic dependencies
- **Migration State** - Tracking table exists, schema is in sync
- **Generated Functions** - All expected functions present, no orphans
- **Generated Client** - When `Options.Client` is set: the generated files
  match a fresh generation, and every name the client uses is installed and
  exposed (`internal/sqlapi.CompareClient`)
- **Tuples Source** - `melange_tuples` view exists with correct columns
- **Data Health** - Tuples reference valid types and relations

## Architecture Role

```
melange doctor
       │
       └── internal/doctor
               │
               ├── pkg/parser (schema parsing)
               ├── pkg/schema (validation)
               ├── pkg/migrator (migration state)
               ├── internal/sqlapi (deployed SQL API, client comparison)
               └── internal/sqlgen (function analysis)
```

The doctor command provides diagnostic output for troubleshooting authorization issues.

## Output

Produces a `Report` with categorized check results:
- `StatusPass` - Check passed
- `StatusWarn` - Non-critical issue
- `StatusFail` - Critical issue that will cause failures

Each failing check includes a `FixHint` suggesting remediation steps.

A hint that suggests `melange migrate` follows what an unforced migrate
would do, from `migrator.Migrator.Plan` (computed only when a check needs
it): a plain migrate when it would apply, naming the dependents of any
removed functions it would refuse to drop; `--force` with the reason when it
would skip or only record, counting the removed functions `--force` drops
with `CASCADE` and naming their dependents. When a newer melange migrated
the database (`migrator.CodegenVersionAhead`), the hint names that version
and never suggests `--force` with this melange, which would override the
downgrade guard. The undecodable-model hint may suggest `--force` with the
newer melange.

`Doctor.CompareClient` runs the client comparison outside `Run` (for
`melange generate client --check --db`), reading the local schema and the
migration record itself, so its fix hints come from the same helpers.

## Key Checks

| Category | Validates |
|----------|-----------|
| Schema File | File exists, syntax valid, no cycles |
| Migration State | Tracking table, schema checksum match |
| Generated Functions | Dispatchers present, no missing/orphan functions |
| Generated Client | `client_file`, `client_functions`, `client_relations`: Warn by default, Fail on an error with `ClientCheck.Strict` |
| Tuples Source | View exists, required columns present |
| Data Health | Tuple types and relations match schema |
