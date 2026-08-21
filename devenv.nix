{
  pkgs,
  config,
  inputs,
  devenv-zsh,
  ...
}: let
  dagger = inputs.dagger.packages.${pkgs.stdenv.hostPlatform.system}.dagger;
in {
  cachix.enable = true;
  cachix.pull = ["pre-commit-hooks"];

  overlays = [
    (_: prev: {
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

  # https://devenv.sh/basics/
  env.KUBECONFIG = "${config.env.DEVENV_STATE}/kube/config";
  env.SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
  # Don't prompt me to sign up for dagger cloud
  env.DAGGER_NO_NAG = "1";

  enterShell = ''
    mkdir -p "$(dirname "$KUBECONFIG")"
    sops decrypt "$DEVENV_ROOT/config/kubeconfig/production.enc.yaml" --output "$KUBECONFIG"
  '';

  # https://devenv.sh/packages/
  packages = with pkgs;
    [
      cacert
      age
      argocd
      cue
      dagger
      deploy-rs
      go
      golangci-lint
      k9s
      kind
      kubecolor
      kubectl
      kubectl-cnpg
      kubeconform
      kubernetes-helm
      kubernetes-polaris
      kubetail
      kustomize
      nixos-anywhere
      opentofu
      sops
      uv
      woodpecker-cli
    ]
    ++ [
      # lab host bootstrap packages
      mkpasswd
      ssh-to-age
    ];

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
    # Python formatter. Deliberately no --line-length: this hook and the Dagger
    # black invocation (.dagger/python.go) both have to produce identical output,
    # and black's default is the one setting neither side can drift on. A flag
    # here would need the exact same flag there, or the two reformat each other
    # forever on every commit.
    black.enable = true;
  };

  # See full reference at https://devenv.sh/reference/options/
}
