#!/usr/bin/env bash
# Converts k3s CA certs to yaml so it can be encrypted with sops
#
set -euo pipefail

output_file=""
declare -A k3s_secrets=(
  [k3s_token]=/var/lib/rancher/k3s/server/token
  [server_ca_crt]=/var/lib/rancher/k3s/server/tls/server-ca.crt
  [server_ca_key]=/var/lib/rancher/k3s/server/tls/server-ca.key
  [client_ca_crt]=/var/lib/rancher/k3s/server/tls/client-ca.crt
  [client_ca_key]=/var/lib/rancher/k3s/server/tls/client-ca.key
  [request_header_ca_crt]=/var/lib/rancher/k3s/server/tls/request-header-ca.crt
  [request_header_ca_key]=/var/lib/rancher/k3s/server/tls/request-header-ca.key
  [etcd_peer_ca_crt]=/var/lib/rancher/k3s/server/tls/etcd/peer-ca.crt
  [etcd_peer_ca_key]=/var/lib/rancher/k3s/server/tls/etcd/peer-ca.key
  [etcd_server_ca_crt]=/var/lib/rancher/k3s/server/tls/etcd/server-ca.crt
  [etcd_server_ca_key]=/var/lib/rancher/k3s/server/tls/etcd/server-ca.key
  [service_key]=/var/lib/rancher/k3s/server/tls/service.key
)

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] path/to/k3s/secrets.enc.yaml"
}

for i in "$@"; do
  case $i in
  -h | --help)
    usage
    exit 0
    ;;
  -v | --verbose)
    set -x
    shift
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "Unknown option $i"
    usage
    exit 1
    ;;
  *)
    if [ -z "$output_file" ]; then
      output_file=$i
      shift
    else
      echo "Unexpected argument $i"
      usage
      exit 1
    fi
    ;;
  esac
done

if [ -z "$output_file" ]; then
  echo "Error: Required argument 'path/to/k3s/secrets.enc.yaml' not present."
  usage
  exit 1
fi

tmp_output="$(mktemp)"
trap 'rm -rf "$tmp_output"' EXIT

for sops_key in "${!k3s_secrets[@]}"; do
  secret_path="${k3s_secrets[${sops_key}]}"
  # shellcheck disable=SC2034
  secret_value="$(ssh borg-0 "sudo cat $secret_path")"
  export secret_value sops_key
  yq --inplace '.[strenv(sops_key)] = strenv(secret_value)' "$tmp_output"
done

write_output() {
  echo "Writing encrypted secrets to $output_file"
  mv "$tmp_output" "$output_file"
  sops encrypt --in-place "$output_file"
  echo "Done!"
}

if [ -e "$output_file" ]; then
  diff_output="$(diff <(yq '.' <(sops decrypt "$output_file")) "$tmp_output")"
  diff_result=$?
  if [ $diff_result -ne 0 ]; then
    echo "Diff between current and new k3s secrets yaml:"
    echo "$diff_output"
    read -p "Do you want to replace $output_file with updated contents? [y/N]: " -r
    if [[ $REPLY =~ ^[Yy]$ ]]; then
      write_output
    else
      echo "Cancelling operation"
    fi
  else
    echo "No changes between current and updated secrets files"
  fi
else
  write_output
fi
