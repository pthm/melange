---
title: Installation
weight: 1
prev: /docs/getting-started
next: /docs/getting-started/quick-start
---

Install the Melange CLI for schema validation, migrations, and code generation.

## Prerequisites

- **PostgreSQL 14 or later**. Melange generates PL/pgSQL functions that require PostgreSQL 14+.

## Install the CLI

{{< tabs >}}

{{< tab name="Homebrew" >}}
```bash
brew install pthm/tap/melange
```
{{< /tab >}}

{{< tab name="Go" >}}
```bash
go install github.com/pthm/melange@latest
```

Requires Go 1.21 or later.
{{< /tab >}}

{{< tab name="Binary" >}}
Download pre-built binaries from the [GitHub releases page](https://github.com/pthm/melange/releases).

Binaries are available for Linux (amd64, arm64) and macOS (amd64, arm64).
{{< /tab >}}

{{< /tabs >}}

Verify the installation:

```bash
melange version
```

## Updating

Melange checks for updates in the background and prints a notice when a newer version is available. The check is non-blocking, cached for 24 hours, and disabled in CI environments (when the `CI` environment variable is set).

To update:

{{< tabs >}}

{{< tab name="Homebrew" >}}
```bash
brew upgrade melange
```
{{< /tab >}}

{{< tab name="Go" >}}
```bash
go install github.com/pthm/melange@latest
```
{{< /tab >}}

{{< /tabs >}}

To disable update notifications:

```bash
melange --no-update-check migrate
```

## Next Steps

- [Quick Start](../quick-start) to run your first permission check
- [Project Setup](../project-setup) for configuration details
