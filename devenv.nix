{
  pkgs,
  config,
  ...
}: {
  cachix.enable = true;
  cachix.pull = ["pre-commit-hooks"];
  overlays = [
    (_: prev: rec {
      kubernetes-helm = prev.wrapHelm prev.kubernetes-helm {
        plugins = with prev.kubernetes-helmPlugins; [
          helm-secrets
          helm-diff
          helm-s3
          helm-git
        ];
      };

      helmfile-wrapped = prev.helmfile-wrapped.override {
        inherit (kubernetes-helm) pluginsDir;
      };
    })
  ];
  # https://devenv.sh/basics/
  env.KUBECONFIG = "${config.env.DEVENV_STATE}/kube/config";

  # https://devenv.sh/packages/
  packages = with pkgs; [
    age
    argocd
    deploy-rs
    helmfile-wrapped
    k9s
    kubecolor
    kubectl
    kubetail
    kubernetes-helm
    kustomize
    nixos-anywhere
    opentofu
    sops
    (writeShellApplication {
      name = "bootstrap-host";
      runtimeInputs = [yq-go sops ssh-to-age mkpasswd];
      text = builtins.readFile ./scripts/bootstrap-host.sh;
    })
    (writeShellApplication {
      name = "deploy-diff";
      runtimeInputs = [deploy-rs];
      text = ''
        if [ $# -gt 1 ]; then
          host=$2
        else
          host=$1
        fi
        set -eou pipefail

        mkfifo wait.fifo
        trap 'rm wait.fifo' EXIT

        deploy --auto-rollback false --debug-logs --skip-checks -- ".#$1" 2>&1 \
          | tee >(grep -v DEBUG) >(grep 'activate-rs --debug-logs activate' | \
              sed -e 's/^.*activate-rs --debug-logs activate \(.*\) --profile-user.*$/\1/' | \
              xargs -I% bash -xc "ssh $host 'nix run --impure nixpkgs#nvd -- --color=always diff /run/current-system %'" ; echo >wait.fifo) \
          >/dev/null

        read -r <wait.fifo
      '';
    })
  ];

  # https://devenv.sh/languages/
  # languages.rust.enable = true;

  # https://devenv.sh/processes/
  # processes.dev.exec = "${lib.getExe pkgs.watchexec} -n -- ls -la";

  # https://devenv.sh/services/
  # services.postgres.enable = true;

  # https://devenv.sh/scripts/
  # scripts.hello.exec = ''
  #   echo hello from $GREET
  # '';

  # https://devenv.sh/tasks/
  # tasks = {
  #   "myproj:setup".exec = "mytool build";
  #   "devenv:enterShell".after = [ "myproj:setup" ];
  # };

  # https://devenv.sh/git-hooks/
  git-hooks.hooks = {
    # Nix code formatter
    alejandra = {
      enable = true;
      after = ["deadnix"];
    };
    # Removes nix dead code
    deadnix = {
      enable = true;
      args = ["--edit"];
    };
    # Terraform code formatter
    terraform-format.enable = true;
    # YAML linter
    yamllint.enable = true;
  };

  # See full reference at https://devenv.sh/reference/options/
}
