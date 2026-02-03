# cli

Shared configuration and error handling for the melange CLI.

## Responsibility

This package provides infrastructure shared across CLI commands:

- **Configuration loading** - Discovers and parses `melange.yaml` with proper precedence (flags > env > config > defaults)
- **Exit codes** - Standardized exit codes for different failure modes
- **Error wrapping** - Typed errors that carry exit codes for proper CLI behavior

## Architecture Role

```
cmd/melange/main.go
       │
       ├── internal/cli (config, errors)
       │
       └── individual command handlers
```

The CLI commands use this package to:
1. Load configuration from `melange.yaml` or environment
2. Build database connection strings
3. Return structured errors with appropriate exit codes

## Key Components

- `Config` - Top-level configuration struct with database, generate, migrate, and doctor settings
- `LoadConfig()` - Discovers config file by walking up to `.git` boundary
- `ExitError` - Error type carrying exit code for `os.Exit()`
- Exit code constants (`ExitConfig`, `ExitSchemaParse`, `ExitDBConnect`)

## Configuration Precedence

1. CLI flags (highest)
2. Environment variables (`MELANGE_*`)
3. Config file (`melange.yaml`)
4. Defaults (lowest)
