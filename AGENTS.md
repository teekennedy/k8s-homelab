# Repository Guidelines

## Project Structure
- `cmd/lab/` contains the unified `lab` CLI (Go + cobra) for managing the homelab; built via its own `flake.nix` and available in devenv.
- `config/` holds CUE-based configuration defining the different deployment environments. This config is used by the `lab` cli, Nix configuration, and kubernetes manifests (through helmfile).
- `.dagger/modules/homelab` is the Dagger module (Go SDK) that runs CI checks, installed as the workspace entrypoint in the root `dagger.toml`. Each language or platform (`go`, `python`, `helmfile`, `kubernetes`, …) is an object of its own in it. See `.dagger/README.md`.
- `nix/hosts/<hostname>/` holds host-specific NixOS modules, sops-encrypted `secrets.yaml`, and facter reports; Add configuration to `nix/hosts/common` for shared bits.
- `nix/modules/` provides reusable Nix modules (e.g. `nix/modules/k3s`) that get imported by multiple hosts; extend here before duplicating config.
- `k8s/foundation/`, `k8s/platform/`, and `k8s/apps/` hold Argo CD application definitions (tier app-of-apps live at `k8s/<tier>/application.yaml`).
- `terraform/` contains infrastructure state (OpenTofu) for provisioning external resources
- `scripts/` includes misc helper scripts such as `scripts/create-pr.sh`.

## Build, Test, and Development Commands

`dagger check` runs all CI checks, which include lint, validation, format, test, and build checks for each language used in the repo. Select checks by toolchain and item, e.g. `dagger check go`, `dagger check helmfile --homelab-helmfile-release=traefik` or `dagger check python --homelab-python-project=scripts/tests`; `dagger check -l` lists them. Check results are based on content-addressed caching - a given check is only ran when one of the files it reads gets modified. It is quick and safe to run often.

Formatters, lint auto-fixers, and other checks that modify files are marked as generators. `dagger check` will return an error if a generator shows changes but does _not_ actually modify anything. To apply changes from a generator, run `dagger generate -y` (or select some, e.g. `dagger generate go -y`).

## Commit, Comment, and Documentation Guidelines
Use Conventional Commit messages that include scope whenever possible.

Comments should be concise and scoped to the configuration or code they are commenting on. Do not reference other parts of the system unless the information is critical to understanding the configuration or code in scope. High level design and architecture documentation belongs in README.md files.

## Secrets & Security
In-cluster secrets, such OIDC client secrets or an admin password for an in-cluster service should be auto-generated using [kubernetes-secret-generator] custom resources or annotations whenever possible. Secrets from terraform provisioned external resources should be added to the cluster using the `kubernetes_secret_v1` resource. Secrets used by NixOS configuration and services should be managed exclusively through `sops`. Never commit decrypted secrets — prefer referencing `config.sops.secrets.<name>.path` from modules.

## Release Automation
- Container releases share `.dagger/modules/homelab/releases.json`, `scripts/release.sh`, and `.woodpecker/release-{tag,}.yaml`. Each project defines a working directory, git-cliff config, and artifact tag prefixes. `dagger api call release publish` selects the build from that manifest.
- Devenv uses root `cliff.toml`. One semver bump creates `devenv-ci-<version>` (ci) and `devenv-shell-<version>` (ci + interactive). Only `devenv.nix`, `devenv.yaml`, `devenv.lock`, and `cmd/lab/**` trigger devenv releases.
- Forgejo resources uses `k8s/platform/forgejo/files/forgejo-resources/cliff.toml` and publishes `forgejo-resources-<version>`, starting at 1.0.0. The first release includes source history from before the directory rename.
- The OpenHands sandbox uses `k8s/apps/openhands/files/sandbox/cliff.toml` and publishes `openhands-sandbox-<version>`, starting at 0.2.0.
- The `release-bot` Forgejo user (in the Bots team) and its Woodpecker secret `release_bot_token` (push and tag pipelines only) are declarative in the Forgejo and Woodpecker chart values. The secret is restricted to the `alpine/git` image so no other step can use it; don't reuse that image elsewhere. Deploy Forgejo resources before Woodpecker resources.
- `dagger api call release publish --tag=devenv-ci-1.2.3 --username=teekennedy --token=env:GHCR_ACCESS_TOKEN` builds and pushes through Dagger. `build-containers` (or `dagger api call release build-dockerfiles`) builds all Dockerfiles, also included in `dagger check`. `export-image` only exports to the host image store.
