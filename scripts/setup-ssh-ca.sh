#!/usr/bin/env bash
# Publishes the deploybot SSH user CA public key from the cluster into
# nix/modules/selfupdate/, which is what the hosts trust via TrustedUserCAKeys.
#
# The CA keypair itself is generated in-cluster by the deploybot-ssh-ca-bootstrap
# ArgoCD sync hook (k8s/foundation/cert-system/templates/deploybot-ssh-ca.yaml);
# the private half never leaves the cluster. This script only copies out the
# public half. Run it once, commit the result, and deploy the hosts.
#
# Idempotent: does nothing if the public key file already matches the cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
CA_PUB="$REPO_ROOT/nix/modules/selfupdate/deploybot_user_ca.pub"

NAMESPACE="${NAMESPACE:-cert-system}"
SECRET_NAME="${SECRET_NAME:-deploybot-ssh-ca}"

usage() {
  echo "Usage: $0 [-h|--help]"
  echo ""
  echo "Copies the deploybot SSH user CA public key out of the cluster and into"
  echo "nix/modules/selfupdate/deploybot_user_ca.pub."
  echo ""
  echo "Environment:"
  echo "  NAMESPACE     Namespace holding the CA secret (default: cert-system)"
  echo "  SECRET_NAME   Name of the CA secret (default: deploybot-ssh-ca)"
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if ! kubectl -n "$NAMESPACE" get secret "$SECRET_NAME" >/dev/null 2>&1; then
  echo "Error: secret $NAMESPACE/$SECRET_NAME not found." >&2
  echo "The deploybot-ssh-ca-bootstrap sync hook creates it; check that the" >&2
  echo "cert-system Application has synced." >&2
  exit 1
fi

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

kubectl -n "$NAMESPACE" get secret "$SECRET_NAME" \
  -o jsonpath='{.data.ca\.pub}' | base64 -d >"$WORK_DIR/ca.pub"

if [[ ! -s "$WORK_DIR/ca.pub" ]]; then
  echo "Error: secret $NAMESPACE/$SECRET_NAME has no ca.pub key." >&2
  exit 1
fi

# Sanity check that what came out is actually an OpenSSH public key, so a
# mangled secret cannot silently become the thing every host trusts.
if ! ssh-keygen -l -f "$WORK_DIR/ca.pub" >/dev/null 2>&1; then
  echo "Error: the ca.pub in $NAMESPACE/$SECRET_NAME is not a valid SSH public key." >&2
  exit 1
fi

if [[ -f "$CA_PUB" ]] && cmp -s "$WORK_DIR/ca.pub" "$CA_PUB"; then
  echo "$CA_PUB is already up to date. Nothing to do."
  exit 0
fi

if [[ -f "$CA_PUB" ]]; then
  # Replacing this locks CI out of every host that still trusts the old CA
  # until each one has been redeployed, so make the operator say so explicitly.
  echo "Warning: $CA_PUB already exists and differs from the cluster's CA." >&2
  echo "Replacing it means no host accepts a deploybot login until every host" >&2
  echo "has been redeployed. Delete the file first if that is really intended." >&2
  exit 1
fi

cp "$WORK_DIR/ca.pub" "$CA_PUB"
echo "Wrote $CA_PUB:"
ssh-keygen -l -f "$CA_PUB"
echo ""
echo "Next:"
echo "  git add nix/modules/selfupdate/deploybot_user_ca.pub"
echo "  lab host deploy borg-2 --boot   # and the other hosts"
echo ""
echo "Until a host has been redeployed it will not accept the deploybot login."
