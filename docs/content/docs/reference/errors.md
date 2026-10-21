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
| `42883` | Undefined function | `ErrMissingFunction` |
| `M2002` | Custom (raised by generated functions) | `ValidationError` with code 2002 |

## Next Steps

- [Go API](../go-api/): full runtime API reference
- [CLI Reference](../cli/): exit codes and command errors
- [Checking Permissions](../../guides/checking-permissions/): error handling patterns in context
