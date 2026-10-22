#!/usr/bin/env bash
# A `dagger` shim that resolves the real CLI from the repo it is invoked against.
#
# Renovate's postUpgradeTasks run dagger to regenerate files after a dependency
# bump, and the generated files depend on the CLI version. To keep them
# consistent, the CLI must be the version the repo expects: a pre-1.0 repo's
# dagger.json engineVersion, or else the in-cluster engine this repo deploys,
# which is also what its CI installs.
#
# This shim "lazy loads" and runs the correct dagger CLI for the current repo.
# This cannot happen in the chart's preCommand, as that step happens before the
# repo has been cloned.
#
# Downloaded CLIs are cached per version under /opt/dagger/versions for the
# lifetime of the pod (the emptyDir), so repeated invocations within one
# renovate run are cached.
set -euo pipefail

engine_values=k8s/platform/dagger-engine/values.yaml
if [ -f dagger.json ]; then
	version="$(jq -re .engineVersion dagger.json)"
elif [ -f "${engine_values}" ]; then
	version="$(sed -n 's|^ *ref:[[:space:]]*registry.dagger.io/engine:||p' "${engine_values}")"
fi
if [ -z "${version:-}" ]; then
	echo "dagger.sh: cannot tell which dagger CLI this repo expects" >&2
	exit 1
fi
bin="/opt/dagger/versions/${version}/dagger"

if [ ! -x "${bin}" ]; then
	mkdir -p "/opt/dagger/versions/${version}"
	# Progress goes to stderr so it cannot corrupt any command's stdout.
	curl -fsSL https://dl.dagger.io/dagger/install.sh |
		DAGGER_VERSION="${version}" BIN_DIR="/opt/dagger/versions/${version}" sh >&2
fi

# Remote Nix builds. In a repo named in NIX_CI_REPOSITORIES whose dagger.toml
# has a `ci` env, dagger runs in that env, which names the borg hosts as the
# untrusted nix-ci login and this agent socket. The agent holds the short-lived
# nix-ci certificate cert-system issues; Dagger is handed the agent, never the
# key. Anything missing leaves the agent empty, and the probe then finds no
# builder and every build runs in the engine, as before.
#
# The agent is started here rather than by a script in the repo, since this
# runs with the pod's credentials for every repo Renovate discovers.
nix_ci_repo() {
	local origin repo
	[ -f dagger.toml ] && grep -q '^\[env\.ci\.' dagger.toml || return 1
	origin="$(git config --get remote.origin.url 2>/dev/null)" || return 1
	origin="${origin%.git}"
	for repo in ${NIX_CI_REPOSITORIES:-}; do
		case "${origin}" in */"${repo}") return 0 ;; esac
	done
	return 1
}

nix_ci_agent() {
	local sock="${NIX_CI_AGENT_SOCK:-/tmp/nix-ci-agent.sock}"
	local secret="${NIX_CI_CERT_SECRET:-nix-ci-ssh-cert}"
	local namespace="${NIX_CI_CERT_NAMESPACE:-woodpecker}"
	local dir cert agent
	dir="$(mktemp -d)"
	if ! cert="$(kubectl -n "${namespace}" get secret "${secret}" -o json 2>/dev/null)" ||
		! jq -re '.data.id_ed25519 | @base64d' <<<"${cert}" >"${dir}/id_ed25519" ||
		! jq -re '.data["id_ed25519-cert.pub"] | @base64d' <<<"${cert}" >"${dir}/id_ed25519-cert.pub"; then
		echo "dagger.sh: cannot read ${namespace}/${secret}; Nix builds run in the Dagger engine" >&2
		rm -rf "${dir}"
		return 0
	fi
	chmod 600 "${dir}/id_ed25519"
	rm -f "${sock}"
	if agent="$(ssh-agent -a "${sock}" -s)" && eval "${agent}" >/dev/null && [ -n "${SSH_AGENT_PID:-}" ]; then
		# shellcheck disable=SC2064 # expand the pid now
		trap "kill ${SSH_AGENT_PID} 2>/dev/null; rm -f '${sock}'" EXIT
		# ssh-add loads the certificate beside the key along with it.
		ssh-add -q "${dir}/id_ed25519" 2>/dev/null ||
			echo "dagger.sh: cannot load the nix-ci certificate; Nix builds run in the Dagger engine" >&2
	else
		echo "dagger.sh: cannot start ssh-agent; Nix builds run in the Dagger engine" >&2
	fi
	rm -rf "${dir}"
}

# `dagger generate` has the --parallel of `dagger check` but not its
# DAGGER_CHECK_PARALLEL default, so give it the same one: the generators'
# containers share the engine pod's memory limit as the checks' do.
args=()
subcommand=
for arg in "$@"; do
	args+=("${arg}")
	if [ -z "${subcommand}" ] && [ "${arg#-}" = "${arg}" ]; then
		subcommand="${arg}"
		if [ "${arg}" = generate ] && [ -n "${DAGGER_CHECK_PARALLEL:-}" ]; then
			case " $* " in
			*" -j "* | *" -j="* | *" --parallel "* | *" --parallel="*) ;;
			*) args+=(--parallel "${DAGGER_CHECK_PARALLEL}") ;;
			esac
		fi
	fi
done
set -- "${args[@]}"

if nix_ci_repo; then
	nix_ci_agent
	# Not exec'd, so that the EXIT trap stops the agent.
	"${bin}" --env ci "$@"
	exit
fi

exec "${bin}" "$@"
