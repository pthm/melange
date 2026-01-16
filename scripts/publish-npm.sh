#!/bin/bash
set -euo pipefail

# Publish to npm with interactive stdin for OTP
# This script ensures npm can prompt for 2FA codes during local releases

# Skip npm publish during snapshot builds
if [[ "${GORELEASER_CURRENT_TAG:-}" == *"-SNAPSHOT-"* ]]; then
    echo "Skipping npm publish for snapshot build"
    exit 0
fi

cd clients/typescript
pnpm build
pnpm publish --access public --no-git-checks </dev/tty
