#!/usr/bin/env bash
# A `dagger` shim that resolves the real CLI from the repo it is invoked against.
#
# Renovate's postUpgradeTasks run `dagger develop` to regenerate the SDK
# bindings after a dependency bump. `dagger develop` rewrites dagger.json's
# engineVersion to whatever the CLI happens to be. To ensure consistent
# generated files, the dagger CLI version must match what's defined in
# the repo's dagger.json.
#
# This shim "lazy loads" and runs the correct dagger CLI for the current repo's
# dagger.json. This cannot happen in the chart's preCommand, as that step
# happens before the repo has been cloned.
#
# Downloaded CLIs are cached per version under /opt/dagger/versions for the
# lifetime of the pod (the emptyDir), so repeated invocations within one
# renovate run are cached.
set -euo pipefail

version="$(jq -re .engineVersion dagger.json)"
bin="/opt/dagger/versions/${version}/dagger"

if [ ! -x "${bin}" ]; then
	mkdir -p "/opt/dagger/versions/${version}"
	# Progress goes to stderr so it cannot corrupt any command's stdout.
	curl -fsSL https://dl.dagger.io/dagger/install.sh |
		DAGGER_VERSION="${version}" BIN_DIR="/opt/dagger/versions/${version}" sh >&2
fi

exec "${bin}" "$@"
