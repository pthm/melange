// Package main provides the melange CLI for managing authorization schemas.
//
// The CLI supports:
//   - validate: Check .fga schema syntax using the OpenFGA parser
//   - generate client: Produce type-safe client code for Go or TypeScript
//   - migrate: Load schema into PostgreSQL (creates tables and functions)
//   - status: Check current migration state
//   - doctor: Run health checks on authorization infrastructure
//   - version: Print version information
//   - license: Print license and third-party notices
//   - config show: Display effective configuration
//
// Configuration can be provided via:
//   - Command-line flags (highest priority)
//   - Environment variables with MELANGE_ prefix
//   - melange.yaml config file (auto-discovered)
//   - Built-in defaults (lowest priority)
//
// This tool is typically run during development and deployment to keep
// the database schema synchronized with .fga files.
//
// Usage:
//
//	melange [flags] <command>
//
// Commands that require database access (migrate, status) need -db or MELANGE_DATABASE_URL.
// Commands that only work with files (validate, generate) do not need database access.
package main

func main() {
	Execute()
}
