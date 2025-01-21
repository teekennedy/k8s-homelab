#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 <hostname> [-- <nixos-anywhere args> ...]"
  echo "  Bootstrap secrets and configuration for a new host."
  echo
  echo "  Arguments:"
  echo "    hostname: Name of the host to bootstrap (required)."
  echo
  echo "  All other arguments are passed on to nixos-anywhere."
}

while [[ $# -gt 0 ]]; do
  case $1 in
  --)
    shift
    break
    ;;
  -*)
    echo "Unknown option $1"
    usage
    exit 1
    ;;
  *)
    if [ -z "${hostname:+x}" ]; then
      hostname="$1"
    fi
    shift # past argument
    ;;
  esac
done

if [ -z "${hostname:+x}" ]; then
  echo "Missing required argument: hostname"
  usage
  exit 1
fi

# Create a temporary directory
temp=$(mktemp -d)

# Function to cleanup temporary directory on exit
cleanup() {
  rm -rf "$temp"
}
trap cleanup EXIT

secrets_yaml_path="./hosts/$hostname/secrets.yaml"
host_ssh_key_path="$temp/persistent/etc/ssh/ssh_host_ed25519_key"
# Generate host secrets.yaml if it doesn't exist
generate_secrets_yaml() {
  local pubkey privkey agekey hashed_password
  ssh-keygen -t ed25519 -C "$hostname" -f "$host_ssh_key_path"

  echo -n "Password for $hostname:"
  read -rs password
  echo

  # shellcheck disable=SC2034
  pubkey="$(cat "$host_ssh_key_path.pub")" privkey="$(cat "$host_ssh_key_path")" agekey="$(ssh-to-age -i "$host_ssh_key_path.pub")" hashed_password=$(mkpasswd --method=SHA-512 "$password")

  yq --null-input --in-place '.keys += [stdenv(agekey) | . anchor = "host_" + stdenv(hostname)]' .sops.yaml
  yq --null-input '.ssh_host_public_key = stdenv(pubkey) | ssh_host_private_key = stdenv(privkey) | .default_user_hashed_password = stdenv(hashed_password)' >"$temp/secrets.yaml"
  sops encrypt --output "$secrets_yaml_path" "$temp/secrets.yaml"
}

# Create the directory where sops-nix expects to find the host keys
install -d -m755 "$(dirname "$host_ssh_key_path")"

# Generate or read ssh key for host
if [ -e "$secrets_yaml_path" ]; then
  sops decrypt "$secrets_yaml_path" | yq '.ssh_host_private_key' >"$host_ssh_key_path"
  sops decrypt "$secrets_yaml_path" | yq '.ssh_host_public_key' >"$host_ssh_key_path.pub"
else
  generate_secrets_yaml
fi

# Set the correct permissions so sshd will accept the key
chmod 600 "$host_ssh_key_path"
chmod 644 "$host_ssh_key_path.pub"

# Install NixOS to the host system with our secrets
set -x
nixos-anywhere --extra-files "$temp" --flake ".#$hostname" "$@"
