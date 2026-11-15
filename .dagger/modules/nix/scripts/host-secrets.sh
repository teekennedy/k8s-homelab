#!/usr/bin/env bash
# Fills in whatever a host's secrets.yaml lacks and writes it sops-encrypted.
#
# Generates an SSH host key pair, hashes HOST_PASSWORD for the default user and
# generates a restic repository password, keeping every field already present.
# The plaintext only ever lives in $WORK, a tmpfs, and nothing secret is
# printed. A complete, already-encrypted file is left byte for byte as it was.
#
# Encrypts to AGE_RECIPIENTS and PGP_RECIPIENTS (comma-separated) plus the
# host's own age key rather than through .sops.yaml, whose rule for the host
# may not exist yet. Writes the host's SSH public key to PUBKEY_OUT.
set -euo pipefail

: "${HOST:?}" "${SECRETS:?}" "${WORK:?}" "${PUBKEY_OUT:?}" "${AGE_RECIPIENTS:?}" "${PGP_RECIPIENTS:?}"

plain=$WORK/secrets.yaml
encrypted=false
if [ -e "$SECRETS" ]; then
  if yq -e 'has("sops")' "$SECRETS" >/dev/null 2>&1; then
    sops decrypt --input-type yaml --output-type yaml "$SECRETS" >"$plain"
    encrypted=true
  else
    cp "$SECRETS" "$plain"
  fi
else
  echo '{}' >"$plain"
fi

missing() {
  [ -z "$(yq ".$1 // \"\"" "$plain")" ]
}

changed=false

if missing ssh_host_public_key || missing ssh_host_private_key; then
  echo "generating the SSH host key pair" >&2
  ssh-keygen -q -t ed25519 -C "$HOST" -N "" -f "$WORK/ssh_host_ed25519_key"
  yq -i "
    .ssh_host_public_key = (load_str(\"$WORK/ssh_host_ed25519_key.pub\") | trim) |
    .ssh_host_private_key = load_str(\"$WORK/ssh_host_ed25519_key\")
  " "$plain"
  changed=true
fi

if missing default_user_hashed_password; then
  if [ -z "${HOST_PASSWORD:-}" ]; then
    echo "secrets.yaml has no default_user_hashed_password: pass --password" >&2
    exit 1
  fi
  echo "hashing the default user's password" >&2
  printf '%s' "$HOST_PASSWORD" | mkpasswd --method=SHA-512 --stdin >"$WORK/hash"
  yq -i ".default_user_hashed_password = (load_str(\"$WORK/hash\") | trim)" "$plain"
  changed=true
fi

if missing restic_repo_password; then
  echo "generating the restic repository password" >&2
  head -c 48 /dev/urandom | base64 -w0 | tr -d '+/=' | head -c 32 >"$WORK/restic"
  yq -i ".restic_repo_password = load_str(\"$WORK/restic\")" "$plain"
  changed=true
fi

yq '.ssh_host_public_key' "$plain" >"$PUBKEY_OUT"

if [ "$changed" = false ] && [ "$encrypted" = true ]; then
  echo "secrets.yaml has every field" >&2
  exit 0
fi

host_age=$(ssh-to-age <"$PUBKEY_OUT")
mkdir -p "$(dirname "$SECRETS")"
# Run from $WORK, which has no .sops.yaml above it: sops applies the repo's
# creation rules even to explicit recipients, and none matches yet.
(
  cd "$WORK"
  sops encrypt --input-type yaml --output-type yaml \
    --age "$AGE_RECIPIENTS,$host_age" --pgp "$PGP_RECIPIENTS" \
    "$plain" >"$WORK/encrypted.yaml"
)
cp "$WORK/encrypted.yaml" "$SECRETS"
echo "wrote encrypted $SECRETS" >&2
