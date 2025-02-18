#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [-h|--help] [-v|--verbose] -c=...|--clients=... -s=...|--server=..."
  echo "  -c, --clients: Comma separated list of client hosts to add, e.g. client1.example.com,client2.example.com"
  echo "                 Note: Include the server in the list of client hosts to allow mounting from localhost."
  echo "  -s, --server: Server host, e.g. client1.example.com"
}

clients=()
server=""
admin_password=""
admin_user="admin/admin"

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
  -c* | --clients*)
    IFS=',' read -ra clients <<<"$(echo $1 | sed -e 's/^[^=]*=//g')"
    shift
    ;;
  -s* | --server*)
    server="$(echo $1 | sed -e 's/^[^=]*=//g')"
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
    echo "Unexpected argument $i"
    usage
    exit 1
    ;;
  esac
done

if [ -z "$server" ]; then
  echo "Missing server argument"
fi

run_ssh() {
  local ssh_url="$1"
  shift
  ssh "$ssh_url" "$@"
}

get_admin_password() {
  if [ -z "$admin_password" ]; then
    # Read Password
    read -rsp "Admin password:" admin_password
    echo
  fi
}

add_principal() {
  local principal="$1"
  if run_ssh "$server" sudo kadmin.local get_principal "$principal" >/dev/null 2>&1; then
    echo "Principal $principal exists"
  else
    echo "Adding principal $principal"
    run_ssh "$server" sudo kadmin.local add_principal -randkey "$principal"
  fi
}

add_admin() {
  local principal="$1"
  if run_ssh "$server" sudo kadmin.local get_principal "$principal" >/dev/null 2>&1; then
    echo "Principal $principal exists"
  else
    echo "Adding admin $principal"
    get_admin_password
    run_ssh "$server" sudo kadmin.local add_principal -pw "$admin_password" "$principal" # set password for admin/admin
  fi
}

# adds a keytab entry for the given host's user to the keytab file of the host
add_keytab_entry() {
  local host="$1"

  if run_ssh "$host" sudo klist -k /etc/krb5.keytab | grep -q "nfs/$host"; then
    echo "Keytab entry for $host exists"
    return
  fi
  echo "Creating keytab entry for $host"
  get_admin_password
  # Follow symlink for keytab path
  keytab_path="$(run_ssh "$host" readlink -f /etc/krb5.keytab || echo "/etc/krb5.keytab")"
  run_ssh "$host" sudo kadmin -p "$admin_user" -w "$admin_password" ktadd -k "$keytab_path" "nfs/$host"
}

if run_ssh "$server" -q [ -e /var/lib/krb5kdc/principal ]; then
  echo "Server kerberos database exists"
else
  echo "Bootstrapping server kerberos database"
  # set up kerberos database
  read -rsp "Master database password:" database_password
  run_ssh "$server" sudo kdb5_util create -s -r MSNG.TO -P "$database_password"
fi

add_admin "admin/admin"

# add principals to server database
for client in "${clients[@]}"; do
  add_principal "nfs/$client"
  add_keytab_entry "$client"
done
