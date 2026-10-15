#!/bin/sh
# Drives a real Nix build whose JSON event stream exercises both halves of
# nix/modules/selfupdate/build-metrics: paths that arrive from a binary
# cache, and a derivation this machine has to build itself.
#
# There is deliberately no /nix cache volume in the container this runs in:
# the assertions this feeds need a cold store. With one, cowsay would already
# be present on a second run and nothing would be substituted.
set -eu
mkdir -p /out
export NIX_CONFIG="experimental-features = nix-command flakes"

# The nixpkgs revision is read out of the repo's own flake.lock rather than
# pinned here, so this follows the fleet's nixpkgs instead of drifting away
# from it, and so a Renovate lock bump exercises the parser against the Nix
# evaluation the hosts will actually run.
rev=$(nix eval --raw --impure --expr \
  'let l = builtins.fromJSON (builtins.readFile /src/flake.lock); in l.nodes.${l.nodes.root.inputs.nixpkgs}.locked.rev')
echo "building against nixpkgs $rev"

# The timings follower is Python and this image has no interpreter. Take one
# from the same pinned nixpkgs, before the log is armed so it stays out of it.
nix build --out-link /tmp/python "github:NixOS/nixpkgs/$rev#python3"

: > /out/nix-log.json
export NIX_CONFIG="$NIX_CONFIG
json-log-path = /out/nix-log.json"

# The follower runs alongside the builds below, exactly as
# nixos-selfupdate.service runs it, so this covers the timestamping path and
# not just the parse.
/tmp/python/bin/python3 /src/nix/modules/selfupdate/build-metrics/nix_build_metrics.py \
  timings --json-log /out/nix-log.json --output /out/timings.txt &
follower=$!

# cowsay is small, has a real closure and is built by Hydra for every
# platform this might run on (including aarch64-linux, which is what the
# engine uses on an Apple-silicon host), so it is guaranteed to substitute.
nix build --no-link "github:NixOS/nixpkgs/$rev#cowsay"

# This derivation builds with /bin/sh and has no dependencies, so it cannot
# be in any binary cache and is guaranteed to produce a local-build event.
# Its builder is a shell loop rather than `sleep` because the image has no
# /bin/sleep and the sandbox is off, so a missing command would silently make
# the build instant -- and an instant build cannot show that timings work.
# The loop runs for around a second, comfortably above the follower's poll
# interval.
nix build --no-link --impure --expr 'derivation {
  name = "nixos-selfupdate-metrics-probe";
  system = builtins.currentSystem;
  builder = "/bin/sh";
  args = ["-c" "i=0; while [ $i -lt 400000 ]; do i=$((i+1)); done; echo probe > $out"];
}'

kill -TERM "$follower" 2>/dev/null || true
wait "$follower" 2>/dev/null || true
