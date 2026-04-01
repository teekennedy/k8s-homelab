{
  pkgs,
  config,
  inputs,
  devenv-zsh,
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
    })
  ];

  # Use zsh for shell instead of bash
  # https://github.com/mcdonc/devenv-zsh
  imports = [devenv-zsh.plugin];
  zsh.enable = true;
  zsh.extraInit = ''
    # Add lab completions to FPATH for zsh
    export FPATH="''${FPATH-}:${lab}/share/zsh/site-functions"
    autoload -Uz _lab
    compdef _lab lab
  '';

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
      go_1_26
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
      woodpecker-cli
    ]);

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

  # See full reference at https://devenv.sh/reference/options/
}
