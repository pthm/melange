---
title: Errors
description: "Error types, sentinel values, and helper functions in Melange's Go runtime package."
weight: 8
---

All error types, sentinel values, and helper functions in the `github.com/pthm/melange/melange` package.

## Sentinel Errors

| Error | Meaning | Common Cause |
|-------|---------|--------------|
| `ErrNoTuplesTable` | `melange_tuples` view or table not found | View not created, wrong search path |
| `ErrInvalidSchema` | Schema parsing failed | Syntax error in `.fga` file |
| `ErrMissingFunction` | Required PostgreSQL function missing | Migration not run, or Melange updated without re-migrating |
| `ErrCyclicSchema` | Cycle detected in relation graph | Circular implied-by chain |
| `ErrBulkCheckDenied` | At least one bulk check was denied | Returned by `BulkCheckResults.AllOrError()` |
| `ErrContextualTuplesUnsupported` | Querier does not support contextual tuples | Using `*sql.DB` instead of `*sql.Tx` or `*sql.Conn` |
| `ErrInvalidContextualTuple` | Contextual tuple failed validation | Malformed or schema-invalid tuple |

All sentinel errors work with `errors.Is`:

```go
allowed, err := checker.Check(ctx, subject, relation, object)
if melange.IsNoTuplesTableErr(err) {
    // melange_tuples view is missing
}
```

## Error Helper Functions

| Function | Checks for |
|----------|------------|
| `IsNoTuplesTableErr(err error) bool` | `ErrNoTuplesTable` |
| `IsInvalidSchemaErr(err error) bool` | `ErrInvalidSchema` |
| `IsMissingFunctionErr(err error) bool` | `ErrMissingFunction` |
| `IsCyclicSchemaErr(err error) bool` | `ErrCyclicSchema` |
| `IsBulkCheckDeniedErr(err error) bool` | `ErrBulkCheckDenied` |
| `IsValidationError(err error) bool` | `ValidationError` |
| `GetValidationErrorCode(err error) int` | Returns code from `ValidationError`, or 0 |

## ValidationError

```go
type ValidationError struct {
    Code    int
    Message string
}
```

Methods:
- `Error() string` implements the `error` interface
- `ErrorCode() int` returns the OpenFGA-compatible error code

### Error Codes

| Code | Constant | Meaning |
|------|----------|---------|
| 2000 | `ErrorCodeValidation` | Invalid request (bad input) |
| 2001 | `ErrorCodeAuthorizationModelNotFound` | Authorization model not found |
| 2002 | `ErrorCodeResolutionTooComplex` | Resolution depth or complexity limit exceeded |

```go
allowed, err := checker.Check(ctx, subject, relation, object)
if melange.IsValidationError(err) {
    code := melange.GetValidationErrorCode(err)
    switch code {
    case melange.ErrorCodeValidation:
        // bad input
    case melange.ErrorCodeResolutionTooComplex:
        // depth limit hit
    }
}
```

## BulkCheckDeniedError

```go
type BulkCheckDeniedError struct {
    Subject  Object
    Relation Relation
    Object   Object
    Index    int   // Position in original request order
    Total    int   // Total denied checks in batch
}
```

Methods:
- `Error() string` implements the `error` interface
- `Unwrap() error` returns `ErrBulkCheckDenied`

Returned by `BulkCheckResults.AllOrError()` when at least one check is denied:

```go
err := results.AllOrError()
if err != nil {
    var denied *melange.BulkCheckDeniedError
    if errors.As(err, &denied) {
        log.Printf("denied: %s %s %s (and %d others)",
            denied.Subject, denied.Relation, denied.Object, denied.Total-1)
    }
}
```

## PostgreSQL Error Mapping

The runtime maps PostgreSQL error codes to sentinel errors:

| SQLSTATE | PostgreSQL Error | Mapped To |
|----------|-----------------|-----------|
| `42P01` | Undefined table | `ErrNoTuplesTable` |
| `42883` | Undefined function (`check_permission`, `list_accessible_*`, `explain_permission` or `expand_permission`) | `ErrMissingFunction` |
| `M2002` | Custom (raised by generated functions) | `ValidationError` with code 2002 |
| `M2003` | Custom (raised by a public dispatcher for a relation the model defines but the [exposure](../sql-api/#exposure) does not expose) | `ValidationError` with code 2000 and the database message |

Every `Checker` method that calls a public function maps these codes: `Check`,
`CheckWithContextualTuples`, bulk checks, the `ListObjects*` and
`ListSubjects*` methods, `Explain`, `Expand` and `ExpandRecursive`, whether
the driver reports the error when the query is sent or while its rows are
read.

`M2003` only occurs when the database was migrated with a restricted SQL API
exposure (the `sql:` section of `melange.yaml`). The `ValidationError` message
is the database's, which names the relation and the type:

```go
_, err := checker.Check(ctx, user, "viewer", doc)
if melange.GetValidationErrorCode(err) == melange.ErrorCodeValidation {
    // err.Error() == `relation "viewer" on type "document" is not exposed`
}
```

A relation the model does not define is not an error: checks deny and lists
return nothing. See [SQL API: M2003](../sql-api/#error-code-m2003).

{{< callout type="info" >}}
The `M2003` mapping, and `expand_permission` in the `42883` mapping, are in
the runtimes released after v0.10.3. With v0.10.3 and earlier, `M2003` comes
back as the driver's error wrapped with the function name (match on its
SQLSTATE), and a missing `expand_permission` is not `ErrMissingFunction`.
{{< /callout >}}

### TypeScript client

`@pthm/melange` maps `M2003` and the melange `42883` case on every `Checker`
method that calls a public function, including bulk checks. It does not map
`M2002` or `42P01`:

| SQLSTATE | Thrown |
|----------|--------|
| `M2003` | `ValidationError` with `code` 2000 (`ErrorCodeValidation`) and the database message; the driver error is its `cause` |
| `42883` naming a melange function | `MissingFunctionError`; the driver error is its `cause` |

Other database errors, including `M2002` and `42P01`, are thrown unchanged
as the driver's error; match them on its `code`. The mapping is in releases after
v0.10.3; earlier releases throw the driver's error for both codes.

## Migration Errors

`pkg/migrator` returns typed errors from `Migrate`, `MigrateWithOptions` and
`MigrateFromString`; match them with `errors.As`.

| Error | Meaning |
|-------|---------|
| `*migrator.DeployedModelChangedError` | `IfDeployedChecksum` did not match the deployed schema checksum; nothing was applied |
| `*migrator.DatabaseAheadError` | The functions were generated by a newer melange; nothing was applied (`melange migrate` exits with code 5) |
| `*migrator.DependentObjectsError` | Functions the migration would drop have dependents (views, materialized views, RLS policies, rules, `BEGIN ATOMIC` functions) |

`DependentObjectsError.Dependents` lists each dependent object and the
function it depends on. Migrate checks for dependents before applying
anything. When a dependent is created by another session after that check,
the drop itself fails with SQLSTATE `2BP01` (`dependent_objects_still_exist`),
since an unforced drop does not `CASCADE`; the error is still a
`DependentObjectsError` (its `Err` holds the database error), and the
migration is not recorded. Set `MigrateOptions.Force` (`melange migrate
--force`) to drop the functions with `CASCADE`, dropping the dependents too.
See [Dependent objects](../../guides/migrations/#dependent-objects).

## Next Steps

- [Go API](../go-api/): full runtime API reference
- [CLI Reference](../cli/): exit codes and command errors
- [Checking Permissions](../../guides/checking-permissions/): error handling patterns in context
