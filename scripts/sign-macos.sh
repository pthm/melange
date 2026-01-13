#!/bin/bash
set -euo pipefail

# Sign macOS binaries for distribution
# Usage: ./scripts/sign-macos.sh <binary-path> [--notarize]
#
# Modes:
#   1. 1Password mode (recommended for local dev):
#      Reads credentials from 1Password automatically
#
#   2. Environment variable mode (for CI):
#      QUILL_SIGN_P12, QUILL_SIGN_PASSWORD, etc.
#
#   3. Keychain fallback (signing only, no notarization):
#      Uses codesign directly with keychain identity

BINARY="${1:?Usage: $0 <binary-path> [--notarize]}"
NOTARIZE="${2:-}"
IDENTITY="Developer ID Application: Patt-Tom McDonnell (BK822A9W2Z)"

# 1Password item names
OP_VAULT="Developer"
OP_P12_ITEM="Apple Developer ID"        # Document with .p12 file + password field
OP_P8_ITEM="App Store Connect API Key"  # Document with .p8 file + Issuer ID + Key ID fields

if [[ ! -f "$BINARY" ]]; then
    echo "Error: Binary not found: $BINARY"
    exit 1
fi

# Check if we're on macOS
if [[ "$(uname)" != "Darwin" ]]; then
    echo "Skipping signing: not on macOS"
    exit 0
fi

# Check if this is a darwin binary
if ! file "$BINARY" | grep -q "Mach-O"; then
    echo "Skipping signing: not a Mach-O binary"
    exit 0
fi

# Try to load credentials from 1Password if available
load_from_1password() {
    if ! command -v op &> /dev/null; then
        return 1
    fi

    # Check if signed in to 1Password
    if ! op account list &> /dev/null; then
        echo "1Password CLI available but not signed in. Run 'eval \$(op signin)' first."
        return 1
    fi

    echo "Loading credentials from 1Password..."

    # Create temp directory for secrets
    SECRETS_DIR=$(mktemp -d)
    trap "rm -rf $SECRETS_DIR" EXIT

    # Get P12 document
    if op document get "$OP_P12_ITEM" --vault "$OP_VAULT" --out-file "$SECRETS_DIR/cert.p12" 2>/dev/null; then
        export QUILL_SIGN_P12="$SECRETS_DIR/cert.p12"
    else
        echo "Failed to get P12 document '$OP_P12_ITEM' from 1Password"
        return 1
    fi

    # Read P12 password from the same item
    if QUILL_SIGN_PASSWORD=$(op read "op://${OP_VAULT}/${OP_P12_ITEM}/password" 2>/dev/null); then
        export QUILL_SIGN_PASSWORD
    else
        echo "Failed to read password from '$OP_P12_ITEM' in 1Password"
        return 1
    fi

    # Read notarization credentials if notarizing
    if [[ "$NOTARIZE" == "--notarize" ]]; then
        # Get Key ID and Issuer ID from P8 item
        if QUILL_NOTARY_KEY_ID=$(op read "op://${OP_VAULT}/${OP_P8_ITEM}/Key ID" 2>/dev/null); then
            export QUILL_NOTARY_KEY_ID
        else
            echo "Failed to read 'Key ID' from '$OP_P8_ITEM' in 1Password"
            return 1
        fi

        if QUILL_NOTARY_ISSUER=$(op read "op://${OP_VAULT}/${OP_P8_ITEM}/Issuer ID" 2>/dev/null); then
            export QUILL_NOTARY_ISSUER
        else
            echo "Failed to read 'Issuer ID' from '$OP_P8_ITEM' in 1Password"
            return 1
        fi

        # Get P8 document
        if op document get "$OP_P8_ITEM" --vault "$OP_VAULT" --out-file "$SECRETS_DIR/notary.p8" 2>/dev/null; then
            export QUILL_NOTARY_KEY="$SECRETS_DIR/notary.p8"
        else
            echo "Failed to get P8 document '$OP_P8_ITEM' from 1Password"
            return 1
        fi
    fi

    return 0
}

# Mode 1: CI mode - environment variables already set
if [[ -n "${QUILL_SIGN_P12:-}" ]]; then
    echo "Using CI mode (environment variables)"

# Mode 2: Try 1Password
elif load_from_1password; then
    echo "Using 1Password mode"

# Mode 3: Fallback to codesign with keychain (signing only)
else
    echo "Signing with codesign (keychain mode): $BINARY"
    codesign --sign "$IDENTITY" \
        --options runtime \
        --timestamp \
        --force \
        "$BINARY"

    echo "Verifying signature..."
    codesign --verify --verbose "$BINARY"
    echo "Signed successfully: $BINARY"
    exit 0
fi

# Use quill for signing (and optionally notarization)
if [[ "$NOTARIZE" == "--notarize" ]] && [[ -n "${QUILL_NOTARY_KEY:-}" ]]; then
    echo "Signing and notarizing with quill: $BINARY"
    quill sign-and-notarize "$BINARY" -vv
else
    echo "Signing with quill: $BINARY"
    quill sign "$BINARY" -vv
fi

# Verify signature
echo "Verifying signature..."
codesign --verify --verbose "$BINARY"
echo "Signed successfully: $BINARY"
