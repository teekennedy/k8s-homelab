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
  -v | --verbose)
    set -x
    shift
    ;;
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

secrets_yaml_path="./nix/hosts/$hostname/secrets.yaml"
host_ssh_key_path="$temp/persistent/etc/ssh/ssh_host_ed25519_key"
# Generate host secrets.yaml if it doesn't exist
generate_secrets_yaml() {
  local pubkey privkey agekey hashed_password
  ssh-keygen -t ed25519 -C "$hostname" -N '' -f "$host_ssh_key_path"

  echo -n "Password for $hostname:"
  read -rs password
  echo

  pubkey="$(cat "$host_ssh_key_path.pub")" privkey="$(cat "$host_ssh_key_path")" agekey="$(ssh-to-age -i "$host_ssh_key_path.pub")" hashed_password=$(mkpasswd --method=SHA-512 "$password") host_anchor="host_$hostname"
  export pubkey privkey agekey hashed_password host_anchor

  yq --inplace '.keys += [strenv(agekey) | . anchor = strenv(host_anchor)]' .sops.yaml
  yq --null-input '.ssh_host_public_key = strenv(pubkey) | .ssh_host_private_key = strenv(privkey) | .default_user_hashed_password = strenv(hashed_password)' >"$secrets_yaml_path"
  sops encrypt --in-place "$secrets_yaml_path"
}

# Create directories
install -d -m755 "$(dirname "$host_ssh_key_path")"
mkdir -p "$(dirname "$secrets_yaml_path")"

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
