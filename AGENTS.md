# Repository Guidelines

## Project Structure & Module Organization
- `cmd/lab/` contains the unified `lab` CLI (Go + cobra) for managing the homelab; built via its own `flake.nix` and available in devenv.
  - `cmd/lab/cmd/` - cobra command definitions (root, env, host, k8s, tf, config)
  - `cmd/lab/kubeconfig/` - environment-aware kubeconfig management with sops decryption
  - `cmd/lab/env/` - Kind-based environment management (staging, ephemeral)
  - `cmd/lab/internal/paths/` - XDG Base Directory Specification-compliant path helpers
- `config/` holds CUE-based environment configuration (`schema.cue`, `base.cue`, `production.cue`, `staging.cue`); use `lab config` commands to validate and export.
  - `config/kubeconfig/` - encrypted kubeconfig files per environment (e.g., `production.enc.yaml`)
- `.dagger/` contains Dagger CI/CD pipeline code (Go SDK) for linting, validation, and builds.
- **CI Architecture**: See `docs/ci-architecture.md` for details on how we avoid circular dependencies.
  - Dev shell (`devenv shell`) includes lab pre-built for convenience
  - CI container (`containers.ci` in devenv.nix) excludes lab, allowing CI to build it from scratch
  - This ensures `dagger call build` works even when lab's build is broken
- **XDG Directories** (local state, follows XDG Base Directory Specification):
  - macOS: `~/Library/Application Support/lab/`, `~/Library/Caches/lab/`
  - Linux: `~/.config/lab/`, `~/.cache/lab/`, `~/.local/state/lab/`
  - Each subcommand uses its own subdirectory: `lab/env/`, `lab/k8s/`, etc.
  - Decrypted kubeconfigs: `(XDG_CACHE)/lab/k8s/kubeconfig/`
  - Environment state: `(XDG_STATE)/lab/env/`
- `nix/hosts/<hostname>/` holds host-specific NixOS modules, `secrets.yaml`, and facter reports; keep new nodes under `nix/hosts/common` for shared bits.
- `nix/modules/` provides reusable Nix modules (e.g. `nix/modules/k3s`, `nix/modules/users/defaultUser.nix`) that get imported by each host; extend here before duplicating config.
- `k8s/foundation/`, `k8s/platform/`, and `k8s/apps/` hold Argo CD application definitions (tier app-of-apps live at `k8s/<tier>/application.yaml`).
- `terraform/` contains infrastructure state (OpenTofu) for network storage; `scripts/` includes bootstrap helpers such as `scripts/bootstrap-host.sh`.

## Build, Test, and Development Commands
- `devenv shell` loads the pinned toolchain (Nix, OpenTofu, yamllint, lab CLI with completions); re-run after updating `devenv.nix`.
- `lab config list` shows available environments; `lab config show <env>` displays resolved configuration.
- `lab config validate` validates all CUE configurations; `lab config export <env> <format>` exports to json/yaml/nix/helm/terraform.
- `lab env list` shows all environments (production + any Kind clusters); `lab env status <name>` shows detailed status.
- `lab env create <name>` creates a Kind-based environment; `lab env start/stop <name>` manages lifecycle.
- `lab host list` shows configured hosts; `lab host build/deploy/diff <host>` wraps NixOS and deploy-rs commands.
- `lab k8s --env <env> <command>` manages Kubernetes with environment-specific kubeconfig.
- `lab k8s kubeconfig decrypt <env>` decrypts kubeconfig; `lab k8s kubeconfig cleanup` removes decrypted files.
- `lab k8s diff/sync <app>` manages Kubernetes applications; `lab tf plan/apply <module>` wraps OpenTofu.
- `lab ci all` runs full CI pipeline (lint, validate, build, test); `lab ci build/lint/test/validate` runs individual stages.
- `dagger call all --source=.` runs CI pipeline directly (works even if lab is broken).
- `nix flake check` runs deploy-rs checks to validate host expressions.
- `nix build .#nixosConfigurations.<host>.config.system.build.toplevel` ensures a host builds successfully; swap `<host>` for `borg-0`, etc.
- `deploy -- .#<host>` applies a configuration via deploy-rs once builds pass.
- `tofu -chdir=terraform plan` reviews infra changes; pair with `tofu apply` only after plan review.
- `dagger call all --source=.` runs the full CI pipeline (lint, validate, build) via Dagger.

## Coding Style & Naming Conventions
Use two-space indentation in Nix files and rely on `alejandra` plus `deadnix` (via pre-commit) to format and prune unused definitions. YAML should stay lowercase-kebab keys, validated with `yamllint --strict`. Terraform modules are formatted with `tofu fmt`. CUE files use tabs for indentation and follow the schema definitions in `config/schema.cue`; validate with `lab config validate` or `cue vet`. Go code in `cmd/lab/` follows standard `go fmt` and `go vet`. Follow existing naming patterns (`borg-*` hosts, `*-system` namespaces, Helm release dirs matching namespaces). Favor explicit attribute sets and keep secrets references under `sops` blocks.

## Testing & Validation
Run `nix flake check` and `lab config validate` before every push. Make sure any untracked nix or cue files are staged or nix/cue commands will not be able to read them. For host changes, capture `nix build` output or `deploy -- --dry-activate .#<host>` when validating deployments. Infrastructure tweaks require `terraform fmt -check` and a recorded `plan` in the PR discussion. Helm modifications should be diffed with `scripts/helm-diff-live.sh <env> <release>`. Run `dagger call lint --source=.` to check all linting locally before pushing.

## Commit & Pull Request Guidelines
Commit subjects stay imperative and concise (see `git log` entries like “Add longhorn recurring jobs”); include relevant scope tags when obvious (e.g. `k3s:`). Squash noisy work-in-progress commits locally. PRs should link the motivating issue or change ticket, summarize affected hosts or services, and note verification commands run (`nix flake check`, `terraform plan`). Attach screenshots or logs when touching user-facing apps or dashboards.

## Secrets & Security
Manage secrets exclusively through `sops`; add new host age keys in `.sops.yaml` and run `sops updatekeys`. Use `scripts/bootstrap-host.sh <host>` to create and encrypt initial SSH material. Never commit decrypted secrets—prefer referencing `config.sops.secrets.<name>.path` from modules.
