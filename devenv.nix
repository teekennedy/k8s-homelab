{
  pkgs,
  config,
  lib,
  inputs,
  devenv-zsh,
  ...
}: let
  dagger = inputs.dagger.packages.${pkgs.stdenv.hostPlatform.system}.dagger;
in {
  cachix.enable = true;
  cachix.pull = ["pre-commit-hooks"];

  # Use zsh for shell instead of bash
  # https://github.com/mcdonc/devenv-zsh
  # Imported unconditionally so `zsh.extraInit` (set by cmd/lab/devenv.nix) is
  # always a declared option; the zsh package itself only enters the closure
  # when zsh.enable is true, which only the `interactive` profile sets.
  imports = [devenv-zsh.plugin];

  # https://devenv.sh/basics/
  env.SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";

  # uv ships its own CPython by default, which means every `uv run` in CI
  # re-downloads an interpreter that the profile already provides. Point uv at
  # the Nix one instead. Every pyproject.toml in this repo asks for >=3.11 or
  # >=3.12, so the profile's python3 satisfies all of them; if that ever stops
  # being true, uv fails loudly rather than silently downloading, which is the
  # error we want.
  env.UV_PYTHON = pkgs.python3.interpreter;
  env.UV_PYTHON_DOWNLOADS = "never";
  env.UV_PYTHON_PREFERENCE = "only-system";

  # Base packages: present in every profile, including the container Dagger
  # builds. Keep this list empty of anything a check doesn't need — a package
  # here is paid for by the CI container.
  packages = with pkgs; [
    cacert
  ];

  # https://devenv.sh/profiles/
  #
  # Profiles are opt-in: a bare `devenv shell` gets only the base packages
  # above. Both real entry points name what they want —
  #   .envrc:      use devenv --no-tui --profile ci --profile interactive
  #   .dagger:     devenv ... --profile ci
  # so the CI container never pays for the interactive tooling.
  profiles = {
    # Everything `dagger check` needs, and nothing else. Adding a tool to a
    # Dagger check means adding it here, not pinning another container image.
    ci.module = {
      packages = with pkgs; [
        alejandra
        black
        cue
        deadnix
        git-cliff
        gitMinimal
        go
        golangci-lint
        # Shell utilities the checks themselves shell out to (helm dependency
        # resolution greps Chart.yaml, the kubeconform schema catalog is a
        # tarball). They are already in the container's closure via the shell's
        # stdenv, but the container runs with $DEVENV_PROFILE/bin on PATH rather
        # than the stdenv, so they have to be named here to be reachable.
        gawk
        gnugrep
        gnutar
        gzip
        helmfile-wrapped
        jq
        kubeconform
        kubernetes-helm
        kubernetes-polaris
        opentofu
        python3
        trivy # The successor to tfsec, apparently
        uv
        woodpecker-cli
        yamllint
      ];
    };

    # What the Kubernetes integration workflow needs on top of ci: kubectl to
    # build the ephemeral cluster's kubeconfig, wait on rollouts and prove that
    # kubeconfig points where it should, and curl to fetch the cluster CA.
    # Separate from ci because no `dagger check` has any business holding a
    # Kubernetes client.
    integration.module = {
      packages = with pkgs; [
        curl
        kubectl
      ];
    };

    # Tools for driving the cluster and the lab hosts by hand. None of this is
    # reachable from a Dagger check.
    interactive.module = {
      scripts.build-containers.exec = ''
        ${dagger}/bin/dagger api call release build "$@"
      '';

      packages = with pkgs; [
        age
        argocd
        dagger
        deploy-rs
        forgejo-cli
        # Full git rather than the gitMinimal that prek already pulls into the
        # closure: this is the one on an interactive $PATH, so it should have the
        # perl subcommands. The ci profile uses gitMinimal for release tests
        # without pulling in perl, gettext and the man pages.
        git
        k9s
        kind
        kubecolor
        kubectl
        kubectl-cnpg
        kubetail
        kustomize
        nixos-anywhere
        sops
        # lab host bootstrap packages
        mkpasswd
        ssh-to-age
      ];

      zsh.enable = true;

      env.KUBECONFIG = "${config.env.DEVENV_STATE}/kube/config";
      # Don't prompt me to sign up for dagger cloud
      env.DAGGER_NO_NAG = "1";
      env.DAGGER_X_RELEASE = "v1.0.0-beta.20";

      enterShell = ''
        mkdir -p "$(dirname "$KUBECONFIG")"
        sops decrypt "$DEVENV_ROOT/config/kubeconfig/production.enc.yaml" --output "$KUBECONFIG"
      '';

      # https://devenv.sh/git-hooks/#adding-custom-hooks
      #
      # One hook, delegating to the same checks CI runs, so there is no second
      # copy of the linter list to drift out of sync with .dagger. `dagger
      # check` also runs every +generate function and fails on a non-empty
      # changeset, which is what covers formatting (alejandra, deadnix, black,
      # cue fmt, tofu fmt, gofumpt).
      git-hooks.hooks.dagger-check = {
        enable = true;
        name = "dagger check";
        entry = "${dagger}/bin/dagger --progress=plain check";
        language = "system";
        # Nothing about this hook is per-file: it always runs the whole suite.
        pass_filenames = false;
        always_run = true;
      };

      # The hook config above is a gitignored symlink that only exists where a
      # devenv shell has been entered, so `git worktree add` creates checkouts
      # whose pre-commit hook fails with "config file not found". This
      # post-checkout hook links a worktree's missing config to the main
      # checkout's. A symlink, unlike a copy, is something devenv will update in
      # place if a shell is later entered in that worktree.
      tasks."homelab:git-hooks:post-checkout" = let
        marker = "managed by devenv task homelab:git-hooks:post-checkout";
        hook = pkgs.writeText "post-checkout" ''
          #!/bin/sh
          # ${marker}
          config=.pre-commit-config.yaml
          [ -e "$config" ] && exit 0
          common_dir="$(git rev-parse --path-format=absolute --git-common-dir)" || exit 0
          main_config="''${common_dir%/.git}/$config"
          [ -e "$main_config" ] && ln -sfn "$main_config" "$config"
          exit 0
        '';
      in {
        exec = ''
          set -eu
          hooks_dir="$(${pkgs.git}/bin/git -C "$DEVENV_ROOT" rev-parse --path-format=absolute --git-path hooks)"
          target="$hooks_dir/post-checkout"
          if [ -e "$target" ] && ! grep -qF ${lib.escapeShellArg marker} "$target"; then
            echo "Not replacing existing $target" >&2
            exit 0
          fi
          cmp -s ${hook} "$target" || install -D -m 0755 ${hook} "$target"
        '';
        after = ["devenv:git-hooks:install"];
        before = ["devenv:enterShell"];
      };
    };
  };

  containers.shell.maxLayers = 100;

  # See full reference at https://devenv.sh/reference/options/
}
