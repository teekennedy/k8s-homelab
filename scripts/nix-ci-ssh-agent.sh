#!/bin/sh
# Source in a Woodpecker step before running the dagger CLI with `--env ci`,
# whose remote Nix builds log in to the borg hosts as nix-ci:
#
#   . scripts/nix-ci-ssh-agent.sh
#
# Starts an SSH agent at the socket dagger.toml's ci env forwards, holding the
# short-lived nix-ci certificate that cert-system reissues every six hours.
# The key is only ever in the agent and this step: Dagger is handed the agent,
# not the key. Needs kubectl and openssh-client.
#
# Never fails the step. Without the agent the probe finds no builder and every
# build runs in the Dagger engine, as it would with no builders at all.

nix_ci_agent() {
  sock=/tmp/nix-ci-agent.sock
  secret=${NIX_CI_CERT_SECRET:-nix-ci-ssh-cert}
  dir=$(mktemp -d) || return

  if ! kubectl get secret "$secret" -o jsonpath='{.data.id_ed25519}' | base64 -d >"$dir/id_ed25519" ||
    ! kubectl get secret "$secret" -o jsonpath='{.data.id_ed25519-cert\.pub}' | base64 -d >"$dir/id_ed25519-cert.pub" ||
    [ ! -s "$dir/id_ed25519" ] || [ ! -s "$dir/id_ed25519-cert.pub" ]; then
    echo "nix-ci: no SSH certificate in secret $secret; Nix builds run in the Dagger engine"
    rm -rf "$dir"
    return
  fi
  chmod 600 "$dir/id_ed25519"

  rm -f "$sock"
  if ! agent=$(ssh-agent -a "$sock" -s) || ! eval "$agent" >/dev/null || [ -z "${SSH_AGENT_PID:-}" ]; then
    echo "nix-ci: could not start ssh-agent; Nix builds run in the Dagger engine"
    rm -rf "$dir"
    return
  fi
  # ssh-add loads the certificate beside the key along with it.
  if ssh-add -q "$dir/id_ed25519" 2>/dev/null; then
    echo "nix-ci: certificate loaded into $sock"
  else
    echo "nix-ci: could not load the certificate; Nix builds run in the Dagger engine"
  fi
  rm -rf "$dir"
}

nix_ci_agent
unset -f nix_ci_agent
