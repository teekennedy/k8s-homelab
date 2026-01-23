{
  pkgs,
  config,
  inputs,
  ...
}: let
  lab = inputs.lab.packages.${pkgs.stdenv.hostPlatform.system}.default;
  dagger = inputs.dagger.packages.${pkgs.stdenv.hostPlatform.system}.dagger;
in {
  cachix.enable = true;
  cachix.pull = ["pre-commit-hooks"];

  overlays = [
    (_: prev: {deploy-rs = inputs.deploy-rs.outputs.packages.${prev.stdenv.hostPlatform.system}.deploy-rs;})
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
  # Don't prompt me to sign up for dagger cloud
  env.DAGGER_NO_NAG = "1";

  # https://devenv.sh/packages/
  packages =
    [
      lab
    ]
    ++ (with pkgs; [
      age
      argocd
      cue
      dagger
      deploy-rs
      go
      helmfile-wrapped
      k9s
      kind
      kubecolor
      kubectl
      kubernetes-helm
      kubetail
      kustomize
      nixos-anywhere
      opentofu
      sops
      uv
    ]);

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

  # Shell hook to set up lab CLI completions
  enterShell = ''
    # Detect shell and source appropriate completions
    if [[ -n "$ZSH_VERSION" ]]; then
      source <(lab completion zsh)
    elif [[ -n "$BASH_VERSION" ]]; then
      source <(lab completion bash)
    fi
  '';

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
    # Python formatter
    black.enable = true;
  };

  # https://devenv.sh/containers/
  # CI container - does NOT include lab to avoid circular dependency
  # This allows dagger to build lab as part of the CI pipeline
  containers.ci = {
    name = "homelab-ci";
    startupCommand = config.processes.processManager.process-compose.configFile;

    # Copy base packages but exclude lab
    copyToRoot = pkgs.buildEnv {
      name = "homelab-ci-root";
      paths = with pkgs; [
        # CI tools
        dagger
        go
        git

        # Nix tooling for building
        nix
        nixos-rebuild
        deploy-rs

        # Kubernetes tools
        argocd
        helmfile-wrapped
        k9s
        kind
        kubecolor
        kubectl
        kubernetes-helm
        kubetail
        kustomize

        # Infrastructure tools
        opentofu
        nixos-anywhere

        # Config/secret tools
        age
        cue
        sops

        # Python for scripts
        uv
      ];
    };
  };

  # See full reference at https://devenv.sh/reference/options/
}
