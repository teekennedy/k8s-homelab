#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 <hostname>"
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -s | --ssh)
    ssh_dest="$2"
    shift # past argument
    shift # past value
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
    elif [ -z "${ip:+x}" ]; then
      ip="$1"
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

if [ -z "${ssh_dest:+x}" ]; then
  ssh_dest="root@$hostname"
fi

# Create a temporary directory
temp=$(mktemp -d)

# Function to cleanup temporary directory on exit
cleanup() {
  rm -rf "$temp"
}
trap cleanup EXIT

# Generate ssh key if it doesn't exist
# if [ ... ] TODO

# Create the directory where sshd expects to find the host keys
install -d -m755 "$temp/etc/ssh"

# Decrypt your private key from the password store and copy it to the temporary directory
sops decrypt "./hosts/$hostname/secrets.yaml" | yq .ssh_host_private_key >"$temp/etc/ssh/ssh_host_ed25519_key"

# Set the correct permissions so sshd will accept the key
chmod 600 "$temp/etc/ssh/ssh_host_ed25519_key"

# Install NixOS to the host system with our secrets
set -x
nixos-anywhere --extra-files "$temp" --flake ".#$hostname" --target-host "$ssh_dest" "$@"
