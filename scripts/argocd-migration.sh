#!/usr/bin/env bash

set -euo pipefail

SCRIPT_NAME=$(basename "$0")
VALUES_FILE="${VALUES_FILE:-foundation/argocd/values.yaml}"
K8S_ROOT="${K8S_ROOT:-k8s}"
LOG_ROOT="${LOG_ROOT:-tmp/argocd-migration}"
DEFAULT_APPSET_NAME="${APPSET_NAME:-root}"
DEFAULT_CLUSTER_NAME="${CLUSTER_NAME:-in-cluster}"
DEFAULT_NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"
DEFAULT_PROJECT="${ARGOCD_PROJECT:-default}"
DEFAULT_ROOT_APP_NAME="${ROOT_APP_NAME:-k8s-root}"

usage() {
  cat <<EOF
Usage: $SCRIPT_NAME [-v|--verbose] <subcommand> [subcommand...]

Subcommands:
  preflight             Take snapshots of current git and Argo CD state.
  safety-toggles        Disable prune/self-heal on the ApplicationSet for migration safety.
  add-plumbing          Scaffold k8s/ tree and ensure root Application entry in values.yaml.
  validate-plumbing     Run lightweight validation on new manifests/values.
  migrate-app           Move an app into k8s/<tier> and rewrite its Application source path.
  decommission-appset   Remove the ApplicationSet block from values.yaml.
  revert-safety         Re-enable prune/self-heal on the ApplicationSet.
  hygiene               Surface stale references in docs/scripts and run nix flake check.

Examples:
  $SCRIPT_NAME preflight safety-toggles
  $SCRIPT_NAME migrate-app --tier apps --name dashy
EOF
}

trap 'echo "[$SCRIPT_NAME] Error on line $LINENO. Exiting." >&2' ERR

confirm() {
  local prompt="${1:-Continue?} [y/N]: "
  read -r -p "$prompt" reply
  [[ "$reply" == "y" || "$reply" == "Y" ]]
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Missing required command: $cmd" >&2
    exit 1
  fi
}

ensure_repo_root() {
  local root
  if ! root=$(git rev-parse --show-toplevel 2>/dev/null); then
    echo "Not inside a git repository." >&2
    exit 1
  fi
  cd "$root"
}

init_log_dir() {
  mkdir -p "$LOG_ROOT"
  LOG_DIR="${LOG_ROOT}/$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$LOG_DIR"
}

backup_file() {
  local file="$1"
  init_log_dir
  cp "$file" "$LOG_DIR/$(basename "$file").bak"
  echo "Backed up $file to $LOG_DIR"
}

git_dirty() {
  ! git diff --quiet --ignore-submodules --cached || ! git diff --quiet --ignore-submodules
}

ensure_upstream() {
  git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1
}

commit_and_push() {
  if ! git_dirty; then
    echo "No git changes to commit."
    return
  fi

  git status --short
  if ! confirm "Stage, commit, and push the above changes?"; then
    echo "Skipping commit/push."
    return
  fi

  read -r -p "Commit message: " commit_msg
  git add -A
  git commit -m "$commit_msg"

  if ensure_upstream; then
    git push
  else
    echo "No upstream configured for this branch; skipping push." >&2
  fi
}

detect_repo_url() {
  yq -r ".argocd-apps.applicationsets.${DEFAULT_APPSET_NAME}.generators[0].git.repoURL // \"\"" "$VALUES_FILE"
}

detect_revision() {
  yq -r ".argocd-apps.applicationsets.${DEFAULT_APPSET_NAME}.generators[0].git.revision // \"\"" "$VALUES_FILE"
}

ensure_app_of_apps_file() {
  local tier="$1"
  local app_name="$2"
  local repo_url="$3"
  local revision="$4"
  local path="$K8S_ROOT/$tier/application.yaml"

  mkdir -p "$K8S_ROOT/$tier"
  if [ -e "$path" ]; then
    echo "$path already exists; leaving as-is."
    return
  fi

  cat >"$path" <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: $app_name
  namespace: $DEFAULT_NAMESPACE
spec:
  project: $DEFAULT_PROJECT
  destination:
    name: $DEFAULT_CLUSTER_NAME
    namespace: $DEFAULT_NAMESPACE
  source:
    repoURL: "$repo_url"
    targetRevision: "$revision"
    path: "$K8S_ROOT/$tier"
    directory:
      recurse: true
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
      - ApplyOutOfSyncOnly=true
      - RespectIgnoreDifferences=true
EOF
  echo "Created $path"
}

ensure_root_application_entry() {
  local repo_url="$1"
  local revision="$2"

  yq -i '.argocd-apps.applications = (.argocd-apps.applications // [])' "$VALUES_FILE"
  if yq -e ".argocd-apps.applications[] | select(.name == \"$DEFAULT_ROOT_APP_NAME\")" "$VALUES_FILE" >/dev/null 2>&1; then
    echo "Root application $DEFAULT_ROOT_APP_NAME already present in $VALUES_FILE"
    return
  fi

  yq -i "
    .argocd-apps.applications += [{
      name: \"$DEFAULT_ROOT_APP_NAME\",
      namespace: \"$DEFAULT_NAMESPACE\",
      project: \"$DEFAULT_PROJECT\",
      destination: { name: \"$DEFAULT_CLUSTER_NAME\", namespace: \"$DEFAULT_NAMESPACE\" },
      source: {
        repoURL: \"$repo_url\",
        targetRevision: \"$revision\",
        path: \"$K8S_ROOT\",
        directory: { recurse: true }
      },
      syncPolicy: { syncOptions: [\"CreateNamespace=true\", \"ApplyOutOfSyncOnly=true\", \"RespectIgnoreDifferences=true\"] }
    }]
  " "$VALUES_FILE"
  echo "Added root Application $DEFAULT_ROOT_APP_NAME to $VALUES_FILE"
}

preflight() {
  require_cmd git
  require_cmd yq
  ensure_repo_root

  init_log_dir
  git status --short >"$LOG_DIR/git-status.txt"
  git rev-parse --abbrev-ref HEAD >"$LOG_DIR/git-branch.txt"
  git rev-parse HEAD >"$LOG_DIR/git-head.txt"

  if [ -f "$VALUES_FILE" ]; then
    cp "$VALUES_FILE" "$LOG_DIR/$(basename "$VALUES_FILE")"
  fi

  if command -v argocd >/dev/null 2>&1; then
    if ! argocd app list >"$LOG_DIR/argocd-app-list.txt"; then
      echo "Warning: failed to run argocd app list (not logged in?)." >&2
    fi
    if [ -n "$DEFAULT_APPSET_NAME" ]; then
      if argocd appset get "$DEFAULT_APPSET_NAME" -o yaml >"$LOG_DIR/appset-${DEFAULT_APPSET_NAME}.yaml"; then
        :
      else
        echo "Warning: unable to snapshot ApplicationSet $DEFAULT_APPSET_NAME via argocd; will try kubectl if available." >&2
        if command -v kubectl >/dev/null 2>&1; then
          kubectl -n "$DEFAULT_NAMESPACE" get applicationset "$DEFAULT_APPSET_NAME" -o yaml >"$LOG_DIR/appset-${DEFAULT_APPSET_NAME}.yaml" || {
            echo "Warning: kubectl could not fetch ApplicationSet $DEFAULT_APPSET_NAME" >&2
          }
        fi
      fi
    fi
  else
    echo "argocd CLI not found; skipping cluster snapshots." >&2
  fi

  echo "Preflight snapshots written to $LOG_DIR"
}

safety_toggles() {
  require_cmd yq
  ensure_repo_root

  if [ ! -f "$VALUES_FILE" ]; then
    echo "Missing $VALUES_FILE; cannot apply safety toggles." >&2
    exit 1
  fi

  echo "This will disable prune/selfHeal on ApplicationSet $DEFAULT_APPSET_NAME in $VALUES_FILE."
  if ! confirm "Proceed with safety toggles?"; then
    echo "Aborted."
    return
  fi

  backup_file "$VALUES_FILE"
  if ! yq -e ".argocd-apps.applicationsets | has(\"$DEFAULT_APPSET_NAME\")" "$VALUES_FILE" >/dev/null 2>&1; then
    echo "ApplicationSet $DEFAULT_APPSET_NAME not found in $VALUES_FILE; nothing to toggle." >&2
    return
  fi

  yq -i "
    .argocd-apps.applicationsets.$DEFAULT_APPSET_NAME.template.spec.syncPolicy.automated.prune = false |
    .argocd-apps.applicationsets.$DEFAULT_APPSET_NAME.template.spec.syncPolicy.automated.selfHeal = false
  " "$VALUES_FILE"
  echo "Safety toggles applied to $VALUES_FILE"
  commit_and_push
}

add_plumbing() {
  require_cmd yq
  ensure_repo_root

  local repo_url revision
  repo_url="${REPO_URL:-$(detect_repo_url)}"
  revision="${REVISION:-$(detect_revision)}"

  if [ -z "$repo_url" ] || [ -z "$revision" ]; then
    echo "Unable to detect repoURL or revision from $VALUES_FILE. Set REPO_URL and REVISION env vars and re-run." >&2
    exit 1
  fi

  ensure_app_of_apps_file "apps" "apps-root" "$repo_url" "$revision"
  ensure_app_of_apps_file "platform" "platform-root" "$repo_url" "$revision"
  ensure_app_of_apps_file "foundation" "foundation-root" "$repo_url" "$revision"
  ensure_root_application_entry "$repo_url" "$revision"
  commit_and_push
}

validate_plumbing() {
  require_cmd yq
  ensure_repo_root

  find "$K8S_ROOT" -maxdepth 2 -name application.yaml -print0 2>/dev/null | while IFS= read -r -d '' file; do
    yq e 'true' "$file" >/dev/null
  done
  yq e 'true' "$VALUES_FILE" >/dev/null

  if command -v helmfile >/dev/null 2>&1; then
    echo "Running helmfile template for Argo CD (no changes applied)..."
    if ! helmfile -f foundation/argocd/helmfile.yaml template >/dev/null; then
      echo "Warning: helmfile template failed; review output above." >&2
    fi
  else
    echo "helmfile not found; skipping helm template validation." >&2
  fi

  echo "Validation complete."
}

migrate_app() {
  require_cmd yq
  ensure_repo_root

  local tier="" app="" parent=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --tier)
      tier="$2"
      shift 2
      ;;
    --name)
      app="$2"
      shift 2
      ;;
    --parent)
      parent="$2"
      shift 2
      ;;
    *)
      echo "Unknown option for migrate-app: $1" >&2
      usage
      exit 1
      ;;
    esac
  done

  if [ -z "$tier" ] || [ -z "$app" ]; then
    echo "migrate-app requires --tier <apps|platform|foundation> and --name <app>." >&2
    exit 1
  fi

  local src="$tier/$app"
  local dest="$K8S_ROOT/$tier/$app"
  local manifest="$dest/application.yaml"

  if [ -e "$dest" ]; then
    echo "Destination $dest already exists; refusing to overwrite." >&2
    exit 1
  fi

  if [ ! -d "$src" ]; then
    echo "Source directory $src not found." >&2
    exit 1
  fi

  mkdir -p "$(dirname "$dest")"
  if ! confirm "Move $src to $dest and rewrite its Application path?"; then
    echo "Aborted."
    return
  fi

  echo "Moving $src -> $dest via git mv"
  git mv "$src" "$dest"

  if [ ! -f "$manifest" ]; then
    echo "Expected Application manifest $manifest not found after move." >&2
    exit 1
  fi

  yq -i ".spec.source.path = \"$dest\"" "$manifest"
  yq e 'true' "$manifest" >/dev/null

  if [ -n "$parent" ] && command -v argocd >/dev/null 2>&1; then
    echo "Running argocd app diff for parent $parent (no sync)..."
    argocd app diff "$parent" --revision HEAD || true
  else
    echo "Skipping argocd diff (parent not provided or argocd CLI missing)."
  fi

  commit_and_push
}

decommission_appset() {
  require_cmd yq
  ensure_repo_root

  if ! confirm "Delete ApplicationSet $DEFAULT_APPSET_NAME from $VALUES_FILE?"; then
    echo "Aborted."
    return
  fi

  backup_file "$VALUES_FILE"
  if ! yq -e ".argocd-apps.applicationsets | has(\"$DEFAULT_APPSET_NAME\")" "$VALUES_FILE" >/dev/null 2>&1; then
    echo "ApplicationSet $DEFAULT_APPSET_NAME not found in $VALUES_FILE; nothing to remove."
    return
  fi
  yq -i "del(.argocd-apps.applicationsets.$DEFAULT_APPSET_NAME)" "$VALUES_FILE"
  echo "Removed ApplicationSet $DEFAULT_APPSET_NAME from $VALUES_FILE"
  commit_and_push
}

revert_safety() {
  require_cmd yq
  ensure_repo_root

  echo "This will re-enable prune/selfHeal on ApplicationSet $DEFAULT_APPSET_NAME."
  if ! confirm "Proceed?"; then
    echo "Aborted."
    return
  fi

  backup_file "$VALUES_FILE"
  if ! yq -e ".argocd-apps.applicationsets | has(\"$DEFAULT_APPSET_NAME\")" "$VALUES_FILE" >/dev/null 2>&1; then
    echo "ApplicationSet $DEFAULT_APPSET_NAME not found in $VALUES_FILE; nothing to revert." >&2
    return
  fi
  yq -i "
    .argocd-apps.applicationsets.$DEFAULT_APPSET_NAME.template.spec.syncPolicy.automated.prune = true |
    .argocd-apps.applicationsets.$DEFAULT_APPSET_NAME.template.spec.syncPolicy.automated.selfHeal = true
  " "$VALUES_FILE"
  echo "Safety toggles reverted in $VALUES_FILE"
  commit_and_push
}

hygiene() {
  require_cmd rg
  ensure_repo_root

  echo "Searching for legacy foundation/platform/apps references in docs and scripts..."
  rg -n "foundation/|platform/|apps/" README.md AGENTS.md scripts renovate.json || true

  if command -v nix >/dev/null 2>&1; then
    echo "Running nix flake check..."
    if ! nix flake check; then
      echo "nix flake check failed; review output above." >&2
    fi
  else
    echo "nix not found; skipping nix flake check." >&2
  fi
}

main() {
  local ran=0

  if [[ $# -eq 0 ]]; then
    usage
    exit 1
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
    -v | --verbose)
      set -x
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    preflight)
      preflight
      ran=1
      shift
      ;;
    safety-toggles)
      safety_toggles
      ran=1
      shift
      ;;
    add-plumbing)
      add_plumbing
      ran=1
      shift
      ;;
    validate-plumbing)
      validate_plumbing
      ran=1
      shift
      ;;
    migrate-app)
      shift
      local migrate_args=()
      while [[ $# -gt 0 ]]; do
        case "$1" in
        preflight | safety-toggles | add-plumbing | validate-plumbing | migrate-app | decommission-appset | revert-safety | hygiene | -v | --verbose | -h | --help)
          break
          ;;
        *)
          migrate_args+=("$1")
          shift
          ;;
        esac
      done
      migrate_app "${migrate_args[@]}"
      ran=1
      ;;
    decommission-appset)
      decommission_appset
      ran=1
      shift
      ;;
    revert-safety)
      revert_safety
      ran=1
      shift
      ;;
    hygiene)
      hygiene
      ran=1
      shift
      ;;
    *)
      echo "Unknown subcommand: $1" >&2
      usage
      exit 1
      ;;
    esac
  done

  if [[ $ran -eq 0 ]]; then
    usage
    exit 1
  fi
}

main "$@"
