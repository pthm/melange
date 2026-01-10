# melange

Python client for Melange PostgreSQL authorization.

> **Note:** This package is a placeholder. The Python runtime is not yet implemented.
> Use the Go runtime (`github.com/pthm/melange/melange`) for production workloads.

## Installation

```bash
pip install melange
```

With async database support:
```bash
pip install melange[asyncpg]  # For asyncpg
pip install melange[psycopg]  # For psycopg3
```

## Status

This package provides:
- Type definitions for authorization objects
- Placeholder Checker class (raises NotImplementedError)

The full implementation will include:
- `Checker` class for permission checks
- Async support with asyncpg and psycopg
- Connection pooling support
- Caching layer

## Generated Client Code

You can generate type-safe client code from your schema using the melange CLI:

```bash
melange generate client --runtime python --schema schema.fga --output ./authz/
```

This generates:
- Type constants (`TYPE_USER`, `TYPE_REPOSITORY`)
- Relation constants (`REL_CAN_READ`, `REL_OWNER`)
- Factory functions (`user()`, `repository()`)

## Contributing

See the [main repository](https://github.com/pthm/melange) for contribution guidelines.

## License

MIT
