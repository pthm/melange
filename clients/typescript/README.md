# @pthm/melange

TypeScript client for Melange PostgreSQL authorization.

> **Note:** This package is a placeholder. The TypeScript runtime is not yet implemented.
> Use the Go runtime (`github.com/pthm/melange/melange`) for production workloads.

## Installation

```bash
npm install @pthm/melange
```

## Status

This package provides:
- Type definitions for authorization objects
- Placeholder Checker class (throws "not implemented")

The full implementation will include:
- `Checker` class for permission checks
- `Cache` for caching decisions
- Connection pooling support

## Generated Client Code

You can generate type-safe client code from your schema using the melange CLI:

```bash
melange generate client --runtime typescript --schema schema.fga --output ./src/authz/
```

This generates:
- Type constants (`TYPE_USER`, `TYPE_REPOSITORY`)
- Relation constants (`REL_CAN_READ`, `REL_OWNER`)
- Factory functions (`user()`, `repository()`)

## Contributing

See the [main repository](https://github.com/pthm/melange) for contribution guidelines.

## License

MIT
