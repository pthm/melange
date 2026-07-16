#!/bin/sh
# Verify release prerequisites before goreleaser runs.
# Called by goreleaser's before hook with the release version, e.g.
#   ./scripts/verify-release.sh {{ .Tag }}
set -e

version="$1"

echo "Verifying release prerequisites..."

if [ -z "$version" ]; then
  echo "ERROR: release version not passed (goreleaser before hook must pass the tag)"
  exit 1
fi
if ! echo "$version" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "ERROR: version must be in format vX.Y.Z, got: $version"
  exit 1
fi
echo "  release version: $version"

# In dry-run mode the release job intentionally does NOT bump go.mod or
# package.json (it can't `go mod tidy` against the unpublished runtime tag), so
# the version cross-checks below would spuriously fail. They only guard a real
# release, where the bump ran before goreleaser. Skip them under DRY_RUN.
if [ "${DRY_RUN:-false}" = "true" ]; then
  echo "  dry run: skipping go.mod / package.json version cross-checks (not bumped in dry-run)"
  echo "All release prerequisites verified (dry run)."
  exit 0
fi

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
