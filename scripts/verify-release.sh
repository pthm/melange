#!/bin/sh
# Verify release prerequisites before goreleaser runs.
# This script is called by goreleaser's before hooks.
set -e

echo "Verifying release prerequisites..."

# Check VERSION file exists and has correct format (vX.Y.Z)
if [ ! -f VERSION ]; then
  echo "ERROR: VERSION file not found"
  exit 1
fi

version=$(tr -d '[:space:]' < VERSION)
if ! echo "$version" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "ERROR: VERSION must be in format vX.Y.Z, got: $version"
  exit 1
fi
echo "  VERSION file OK: $version"

# Verify go.mod references correct melange module version
gomod_version=$(awk '$1 == "github.com/pthm/melange/melange" { print $2; exit }' go.mod)
if [ -z "$gomod_version" ]; then
  echo "ERROR: Could not find melange module version in go.mod"
  exit 1
fi
if [ "$gomod_version" != "$version" ]; then
  echo "ERROR: go.mod melange version ($gomod_version) does not match VERSION ($version)"
  echo "Run: just release-prepare VERSION=${version#v}"
  exit 1
fi
echo "  go.mod version OK: $gomod_version"

# Verify package.json version matches VERSION (without v prefix)
npm_expected="${version#v}"
npm_actual=$(grep '"version"' clients/typescript/package.json | sed 's/.*: *"\([^"]*\)".*/\1/')
if [ "$npm_actual" != "$npm_expected" ]; then
  echo "ERROR: package.json version ($npm_actual) does not match VERSION ($npm_expected)"
  echo "Run: just release-prepare VERSION=$npm_expected"
  exit 1
fi
echo "  package.json version OK: $npm_actual"

echo "All release prerequisites verified."
