#!/usr/bin/env bash
# Regenerates k8s/platform/woodpecker/files/known_hosts from the live borg hosts.
#
# Every scanned key is cross-checked against the host's age recipient in
# .sops.yaml before it is written: those age keys are derived from the same
# ed25519 host keys, so a mismatch means either the host was reinstalled (and
# .sops.yaml needs updating too, via `sops updatekeys`) or the scan was
# intercepted. Either way, refusing to write is the right answer.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
KNOWN_HOSTS="$REPO_ROOT/k8s/platform/woodpecker/files/known_hosts"
SOPS_CONFIG="$REPO_ROOT/.sops.yaml"
ENV_JSON="$REPO_ROOT/config/gen/production/env.json"

for tool in ssh-to-age jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Error: $tool not found. Run this inside 'devenv shell', or:" >&2
    echo "  nix shell nixpkgs#ssh-to-age nixpkgs#jq -c $0" >&2
    exit 1
  fi
done

# The fleet inventory comes from the CUE config rather than a copy here; it is
# exported to config/gen/production/env.json by `lab config export`. Only name
# and ip are used -- the k3s roles in that file are known to disagree with
# flake.nix, so nothing should derive ordering or roles from it.
mapfile -t HOSTS < <(jq -r '.hosts[] | "\(.name) \(.ip)"' "$ENV_JSON")

if [[ ${#HOSTS[@]} -eq 0 ]]; then
  echo "Error: no hosts found in $ENV_JSON" >&2
  exit 1
fi

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

{
  cat <<'EOF'
# SSH host keys for the borg nodes, used by CI to verify hosts it deploys to
# (.woodpecker/deploy-hosts.yaml and the nixos-rollout job). This lives in the
# woodpecker chart rather than under nix/ so that one copy serves both readers:
# Helm's .Files.Get cannot reach outside the chart directory, and the pipeline
# reads the same path out of its checked-out workspace.
#
# Host public keys are not secret. These are the same keys .sops.yaml records
# as each host's age recipient, so they can be re-verified without trusting
# this file:
#
#   ssh-keyscan -t ed25519 10.69.80.10 | grep ssh-ed25519 | ssh-to-age
#
# must reproduce the matching host_borg-N age key in .sops.yaml.
#
# Regenerate with scripts/update-known-hosts.sh after rebuilding a host, which
# is the only thing that changes a host key (impermanence persists
# /etc/ssh/ssh_host_ed25519_key across reboots).
EOF

  for entry in "${HOSTS[@]}"; do
    read -r name ip <<<"$entry"

    if ! ssh-keyscan -T 20 -t ed25519 "$ip" 2>/dev/null | grep ssh-ed25519 >"$WORK_DIR/$name.pub"; then
      echo "Error: could not scan an ed25519 host key from $name ($ip)" >&2
      exit 1
    fi

    scanned_age="$(ssh-to-age <"$WORK_DIR/$name.pub")"
    expected_age="$(grep -oE "&host_${name} age1[a-z0-9]+" "$SOPS_CONFIG" | awk '{print $2}')"

    if [[ -z "$expected_age" ]]; then
      echo "Error: no host_${name} age key found in .sops.yaml" >&2
      exit 1
    fi
    if [[ "$scanned_age" != "$expected_age" ]]; then
      echo "Error: host key mismatch for $name ($ip)." >&2
      echo "  scanned:  $scanned_age" >&2
      echo "  .sops.yaml: $expected_age" >&2
      echo "If $name was genuinely reinstalled, update .sops.yaml and run" >&2
      echo "'sops updatekeys' on its secrets first." >&2
      exit 1
    fi

    key="$(cut -d' ' -f2- <"$WORK_DIR/$name.pub")"
    echo "$name,$name.msng.to,$ip $key"
  done
} >"$WORK_DIR/known_hosts"

mv "$WORK_DIR/known_hosts" "$KNOWN_HOSTS"
echo "Wrote $KNOWN_HOSTS (all host keys verified against .sops.yaml)"
