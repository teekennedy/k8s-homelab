#!/usr/bin/env bash
# Installs NixOS on TARGET with nixos-anywhere, handing it the host's SSH key
# from secrets.yaml so the new system can decrypt its secrets. The key is
# only ever written to $WORK, a tmpfs.
set -euo pipefail

: "${HOST:?}" "${SECRETS:?}" "${WORK:?}" "${TARGET:?}"

extra=$WORK/extra-files
ssh_dir=$extra/persistent/etc/ssh
install -d -m 755 "$ssh_dir"
sops decrypt --input-type yaml --output-type yaml "$SECRETS" >"$WORK/secrets.yaml"
yq -e '.ssh_host_private_key' "$WORK/secrets.yaml" >/dev/null
# yq ends a block scalar with an extra newline; $(...) strips them all.
(
  umask 077
  printf '%s\n' "$(yq '.ssh_host_private_key' "$WORK/secrets.yaml")" >"$ssh_dir/ssh_host_ed25519_key"
)
printf '%s\n' "$(yq -e '.ssh_host_public_key' "$WORK/secrets.yaml")" >"$ssh_dir/ssh_host_ed25519_key.pub"
rm "$WORK/secrets.yaml"

ssh_args=()
for o in ${SSH_OPTIONS:-}; do
  ssh_args+=(--ssh-option "$o")
done

nixos-anywhere \
  --extra-files "$extra" \
  --flake ".#$HOST" \
  --target-host "$TARGET" \
  "${ssh_args[@]}"
