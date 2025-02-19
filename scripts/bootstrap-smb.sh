#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] ssh_url"
}

ssh_url=""

for i in "$@"; do
  case $i in
  -h | --help)
    usage
    exit 0
    ;;
  -v | --verbose)
    set -x
    ;;
  --)
    break
    ;;
  -*)
    echo "Unknown option $i"
    usage
    exit 1
    ;;
  *)
    if [ -z "$ssh_url" ]; then
      ssh_url=$i
    else
      echo "Unexpected argument $i"
      usage
      exit 1
    fi
    ;;
  esac
done

ssh "$ssh_url" sudo mkdir -p /storage/nas/k8s
ssh "$ssh_url" sudo chown smb-k8s:smb-k8s /storage/nas/k8s

echo "You will be asked for the same password three times"
ssh "$ssh_url" sudo smbpasswd -a smb-k8s

echo "Setting up namespace and secret"
kubectl create namespace csi-driver-smb

# Read Password
echo -n Password for smb-k8s:
read -rs password
echo
kubectl -n csi-driver-smb create secret generic smbcreds --from-literal=username=smb-k8s "--from-literal=password=$password"
