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

exec "${bin}" "$@"
