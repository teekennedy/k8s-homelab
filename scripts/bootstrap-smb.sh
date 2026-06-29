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

echo "Password for smb-k8s"
ssh "$ssh_url" sudo smbpasswd -a smb-k8s

echo "Password for smb-longhorn"
ssh "$ssh_url" sudo smbpasswd -a smb-longhorn

echo "Setting up namespaces and secrets"

# Read Password
echo -n Password for smb-k8s:
read -rs password
echo

kubectl get namespace/csi-driver-smb >/dev/null 2>&1 || kubectl create namespace csi-driver-smb
kubectl -n csi-driver-smb create secret generic smbcreds \
  --from-literal=username=smb-k8s \
  "--from-literal=password=$password"

echo -n Password for smb-longhorn:
read -rs password
echo
kubectl get namespace/longhorn-system >/dev/null 2>&1 || kubectl create namespace longhorn-system
kubectl -n longhorn-system create secret generic cifs-secret \
  --from-literal=CIFS_USERNAME=smb-longhorn \
  "--from-literal=CIFS_PASSWORD=$password"
