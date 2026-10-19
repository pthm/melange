#!/usr/bin/env bash
# Vendor melange studio's browser dependencies from their official npm
# distributions into internal/studio/assets/npm/<package>/, keeping each
# package's LICENSE and package.json (name, version, license) beside the
# files studio serves. internal/licenses/gen_notice.go reads those into
# THIRD_PARTY_NOTICES, so run `just licenses` after changing a version.
#
#   scripts/studio-assets.sh           fetch the pinned versions below
#   scripts/studio-assets.sh outdated  compare the pins with the npm registry
#
# `npm pack` downloads each tarball from the registry and verifies it
# against the registry's integrity hash before anything is copied.
set -euo pipefail

# "package@version file..." with files relative to the package root.
PACKAGES=(
  "htmx.org@2.0.11 dist/htmx.min.js"
  "@fontsource-variable/space-grotesk@5.3.0 files/space-grotesk-latin-wght-normal.woff2"
  "@fontsource/space-mono@5.3.0 files/space-mono-latin-400-normal.woff2 files/space-mono-latin-700-normal.woff2"
)

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VENDOR="$ROOT/internal/studio/assets/npm"

# dir_of maps an npm name to its vendor directory: @scope/name -> scope-name.
dir_of() { local n="${1#@}"; echo "${n//\//-}"; }

outdated() {
  local stale=0 spec pkg pinned latest
  for entry in "${PACKAGES[@]}"; do
    spec="${entry%% *}"
    pkg="${spec%@*}"
    pinned="${spec##*@}"
    latest="$(npm view "$pkg" version)"
    if [[ "$pinned" == "$latest" ]]; then
      echo "ok        $pkg $pinned"
    else
      echo "outdated  $pkg $pinned -> $latest"
      stale=1
    fi
  done
  return "$stale"
}

fetch() {
  local spec files pkg dest tgz
  tmp="$(mktemp -d)" # global: the EXIT trap runs after fetch returns
  trap 'rm -rf "$tmp"' EXIT
  rm -rf "$VENDOR"
  for entry in "${PACKAGES[@]}"; do
    read -r spec files <<<"$entry"
    pkg="${spec%@*}"
    dest="$VENDOR/$(dir_of "$pkg")"
    tgz="$(cd "$tmp" && npm pack "$spec" --silent)"
    mkdir -p "$tmp/$pkg" "$dest"
    tar -xzf "$tmp/$tgz" -C "$tmp/$pkg"
    for f in $files LICENSE package.json; do
      cp "$tmp/$pkg/package/$f" "$dest/"
    done
    echo "vendored  $spec -> ${dest#"$ROOT"/}"
  done
}

case "${1:-fetch}" in
  fetch) fetch ;;
  outdated) outdated ;;
  *) echo "usage: $0 [fetch|outdated]" >&2; exit 2 ;;
esac
