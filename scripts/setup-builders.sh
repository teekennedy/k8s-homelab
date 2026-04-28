#!/usr/bin/env bash
# Sets up the nixbuilder SSH keypair and cluster package-signing keypair for
# nix/modules/builders. Idempotent: skips generation if the public key file
# already exists in nix/modules/builders/.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
BUILDERS_DIR="$REPO_ROOT/nix/modules/builders"
SOPS_FILE="$BUILDERS_DIR/secrets.enc.yaml"

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

SSH_PUB="$BUILDERS_DIR/nixbuilder_ed25519.pub"
SIGNING_PUB="$BUILDERS_DIR/cluster-signing_ed25519.pub"

NEED_SSH=false
NEED_SIGNING=false

if [[ -f "$SSH_PUB" ]]; then
  echo "nixbuilder SSH key already exists, skipping."
else
  NEED_SSH=true
  echo "Generating nixbuilder SSH keypair..."
  ssh-keygen -t ed25519 -C nixbuilder -f "$WORK_DIR/nixbuilder_ed25519" -N ""
fi

if [[ -f "$SIGNING_PUB" ]]; then
  echo "Cluster signing key already exists, skipping."
else
  NEED_SIGNING=true
  echo "Generating cluster package signing key..."
  nix-store --generate-binary-cache-key borg-1 \
    "$WORK_DIR/cluster-signing_ed25519" \
    "$WORK_DIR/cluster-signing_ed25519.pub"
fi

if ! $NEED_SSH && ! $NEED_SIGNING; then
  echo "All keys already set up. Nothing to do."
  exit 0
fi

# Copy new public keys into the builders module directory
$NEED_SSH    && cp "$WORK_DIR/nixbuilder_ed25519.pub" "$SSH_PUB"
$NEED_SIGNING && cp "$WORK_DIR/cluster-signing_ed25519.pub" "$SIGNING_PUB"

# Write private keys into the sops-encrypted secrets file
if [[ ! -f "$SOPS_FILE" ]]; then
  # No file yet — build a plaintext YAML and encrypt it in one pass
  tmpyaml="$WORK_DIR/secrets.yaml"
  if $NEED_SSH; then
    printf 'nixbuilder_ssh_key: |\n' >> "$tmpyaml"
    sed 's/^/  /' "$WORK_DIR/nixbuilder_ed25519" >> "$tmpyaml"
  fi
  if $NEED_SIGNING; then
    # Single-line value; no block scalar needed
    signing_key="$(cat "$WORK_DIR/cluster-signing_ed25519")"
    printf 'cluster_signing_key: %s\n' "$signing_key" >> "$tmpyaml"
  fi
  sops --encrypt --output "$SOPS_FILE" "$tmpyaml"
else
  # File exists — update individual keys without touching the others.
  # jq -Rs . produces a correctly escaped JSON string literal, handling
  # newlines in the SSH private key.
  if $NEED_SSH; then
    sops set "$SOPS_FILE" '["nixbuilder_ssh_key"]' \
      "$(jq -Rs . < "$WORK_DIR/nixbuilder_ed25519")"
  fi
  if $NEED_SIGNING; then
    sops set "$SOPS_FILE" '["cluster_signing_key"]' \
      "$(jq -Rs . < "$WORK_DIR/cluster-signing_ed25519")"
  fi
fi

echo
echo "Done! Keys written:"
$NEED_SSH     && echo "  SSH public key:     $SSH_PUB"
$NEED_SIGNING && echo "  Signing public key: $SIGNING_PUB"
echo "  Secrets file:       $SOPS_FILE"
echo
echo "Commit nix/modules/builders/ (public keys + secrets.enc.yaml) before deploying."
