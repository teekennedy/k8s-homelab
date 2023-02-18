{
  pkgs,
  config,
  ...
}: {
  env.KUBECONFIG = "${config.env.DEVENV_ROOT}/kubeconfig";
  env.ANSIBLE_CONFIG = "${config.env.DEVENV_ROOT}/ansible.cfg";
  env.ANSIBLE_HOST_KEY_CHECKING = "False";
  env.K8S_AUTH_KUBECONFIG = "${config.env.DEVENV_ROOT}/kubeconfig";
  env.SOPS_AGE_KEY_FILE = ~/.config/sops/age/keys.txt;

  # https://devenv.sh/packages/
  packages = [
    pkgs.age
    pkgs.ansible
    pkgs.fluxcd
    pkgs.go-task
    pkgs.kubernetes-helm
    pkgs.ipcalc
    pkgs.jq
    pkgs.kubectl
    pkgs.kustomize
    pkgs.python3
    pkgs.sops
    pkgs.stern
    pkgs.terraform
    pkgs.yq-go
  ];

  enterShell = ''
    git --version
    echo $SHELL
    . <(flux completion zsh)
  '';

  # https://devenv.sh/languages/
  languages.nix.enable = true;

  # https://devenv.sh/scripts/
  # scripts.hello.exec = "echo hello from $GREET";

  # https://devenv.sh/pre-commit-hooks/
  pre-commit.hooks = {
    # Nix code formatter
    alejandra.enable = true;
    # Terraform formatter
    terraform-format.enable = true;
    # YAML linter
    yamllint.enable = true;
  };

  # https://devenv.sh/processes/
  # processes.ping.exec = "ping example.com";
}
