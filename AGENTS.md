# Repository Guidelines

## Project Structure & Module Organization
- `hosts/<hostname>/` holds host-specific NixOS modules, `secrets.yaml`, and facter reports; keep new nodes under `hosts/common` for shared bits.
- `modules/` provides reusable Nix modules (e.g. `modules/k3s`, `modules/users/defaultUser.nix`) that get imported by each host; extend here before duplicating config.
- `foundation/` defines cluster-wide Helm releases managed via `foundation/helmfile.yaml`; `platform/` layers higher-level services, and `apps/` tracks user-facing workloads.
- `terraform/` contains infrastructure state (OpenTofu) for network storage; `scripts/` includes bootstrap helpers such as `scripts/bootstrap-host.sh`.

## Build, Test, and Development Commands
- `devenv shell` loads the pinned toolchain (Nix, OpenTofu, yamllint); re-run after updating `devenv.nix`.
- `nix flake check` runs deploy-rs checks to validate host expressions.
- `nix build .#nixosConfigurations.<host>.config.system.build.toplevel` ensures a host builds successfully; swap `<host>` for `borg-0`, etc.
- `deploy -- .#<host>` applies a configuration via deploy-rs once builds pass.
- `tofu -chdir=terraform plan` reviews infra changes; pair with `tofu apply` only after plan review.

## Coding Style & Naming Conventions
Use two-space indentation in Nix files and rely on `alejandra` plus `deadnix` (via pre-commit) to format and prune unused definitions. YAML should stay lowercase-kebab keys, validated with `yamllint --strict`. Terraform modules are formatted with `tofu fmt`. Follow existing naming patterns (`borg-*` hosts, `*-system` namespaces, Helm release dirs matching namespaces). Favor explicit attribute sets and keep secrets references under `sops` blocks.

## Testing & Validation
Run `nix flake check` before every push. Make sure any untracked nix files are staged or nix commands will not be able to read them. For host changes, capture `nix build` output or `deploy -- --dry-activate .#<host>` when validating deployments. Infrastructure tweaks require `terraform fmt -check` and a recorded `plan` in the PR discussion. Helm modifications should be diffed with `scripts/helm-diff-live.sh <env> <release>`.

## Commit & Pull Request Guidelines
Commit subjects stay imperative and concise (see `git log` entries like “Add longhorn recurring jobs”); include relevant scope tags when obvious (e.g. `k3s:`). Squash noisy work-in-progress commits locally. PRs should link the motivating issue or change ticket, summarize affected hosts or services, and note verification commands run (`nix flake check`, `terraform plan`). Attach screenshots or logs when touching user-facing apps or dashboards.

## Secrets & Security
Manage secrets exclusively through `sops`; add new host age keys in `.sops.yaml` and run `sops updatekeys`. Use `scripts/bootstrap-host.sh <host>` to create and encrypt initial SSH material. Never commit decrypted secrets—prefer referencing `config.sops.secrets.<name>.path` from modules.
