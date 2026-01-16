#!/bin/bash
set -euo pipefail

# Publish to npm with interactive stdin for OTP
# This script ensures npm can prompt for 2FA codes during local releases

cd clients/typescript
pnpm build
pnpm publish --access public --no-git-checks </dev/tty
