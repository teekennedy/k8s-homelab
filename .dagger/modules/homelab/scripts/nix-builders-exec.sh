#!/bin/sh
# Runs its arguments with an ssh client on PATH, which Nix needs to reach its
# builders. An image that ships none, such as devenv's, gets the Nix image's
# from the binary cache mounted beside this script; copying it is a no-op once
# the store has it.
set -eu

if ! command -v ssh >/dev/null 2>&1; then
  client=/etc/homelab-nix/ssh-client
  pkg=$(cat "$client/path")
  nix --extra-experimental-features nix-command \
    copy --no-check-sigs --from "file://$client/cache" "$pkg"
  PATH="$pkg/bin:$PATH"
  export PATH
fi

exec "$@"
