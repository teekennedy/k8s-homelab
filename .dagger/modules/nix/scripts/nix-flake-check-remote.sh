#!/bin/sh
# Runs `nix flake check` on the flake in the working directory, building its
# checks in a remote builder's store. The arguments are the store options
# (--store, --eval-store, ...) the builds run with.
#
# `nix flake check --store` itself builds nothing: it asks the build store
# which checks are missing before copying their derivations there, and the
# store answers that it knows none of them. `nix build` copies them first.
set -eu

nix() {
  command nix --extra-experimental-features 'nix-command flakes' "$@"
}

nix flake check --no-build

system=$(nix eval --impure --raw --expr builtins.currentSystem)
checks=$(nix eval --raw ".#checks.$system" \
  --apply "checks: toString (map (name: \".#checks.$system.\${name}\") (builtins.attrNames checks))")
if [ -z "$checks" ]; then
  echo "no checks for $system to build"
  exit 0
fi
echo "building $checks"
# shellcheck disable=SC2086 # one installable per word
nix build --no-link --print-build-logs "$@" $checks
