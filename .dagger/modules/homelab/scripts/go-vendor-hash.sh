#!/bin/sh
# Recompute the buildGoModule vendorHash for a nix flake packaging a Go module.
#
# Usage: go-vendor-hash.sh <module-dir> <flake-attr>
#   e.g. go-vendor-hash.sh cmd/lab lab
#
# Writes <module-dir>/gomod.json with the hash nix actually produces for the
# vendored dependencies. Works by writing a known-wrong ("fake") hash first,
# which makes the fixed-output vendor derivation fail with the real hash in its
# error message, before anything else in the package is built.
set -eu

MODULE_DIR="${1:?module dir required}"
FLAKE_ATTR="${2:?flake attribute required}"
HASH_FILE="$MODULE_DIR/gomod.json"
FAKE_HASH="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
LOG=$(mktemp)

write_hash() {
    printf '{\n  "vendorHash": "%s"\n}\n' "$1" >"$HASH_FILE"
}

write_hash "$FAKE_HASH"

if nix --extra-experimental-features 'nix-command flakes' \
    build "./$MODULE_DIR#$FLAKE_ATTR" --no-link >"$LOG" 2>&1; then
    echo "error: expected the fake vendorHash to fail the build, but it succeeded" >&2
    exit 1
fi

# nixpkgs prints "got:    sha256-..." on a fixed-output hash mismatch.
VENDOR_HASH=$(grep -Eo 'got: *sha256-[A-Za-z0-9+/=]+' "$LOG" | tail -n1 | grep -Eo 'sha256-[A-Za-z0-9+/=]+' || true)

if [ -z "$VENDOR_HASH" ]; then
    echo "error: could not parse a vendor hash from the nix build output" >&2
    cat "$LOG" >&2
    exit 1
fi

write_hash "$VENDOR_HASH"
echo "$MODULE_DIR vendorHash: $VENDOR_HASH"
