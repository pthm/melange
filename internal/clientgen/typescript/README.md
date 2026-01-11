# typescript

TypeScript client code generator stub.

## Responsibility

Placeholder for future TypeScript code generation. Currently returns `ErrNotImplemented` when invoked.

## Architecture Role

Registered in the generator registry to provide helpful error messages when users attempt `--runtime typescript`. This allows the CLI to recognize the runtime name without crashing.

## Planned Output

When implemented, will produce:

- `types.ts` - Type constants and TypeScript interfaces
- `schema.ts` - Factory functions for creating objects
- `index.ts` - Re-exports for clean imports
