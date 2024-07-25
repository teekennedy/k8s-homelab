{pkgs, ...}: {
  env.SOPS_AGE_KEY_FILE = ~/.config/sops/age/keys.txt;

  # https://devenv.sh/packages/
  packages = [
    pkgs.age
    pkgs.sops
    pkgs.colmena
  ];

  enterShell = ''
  '';

  # https://devenv.sh/languages/
  languages.nix.enable = true;

  # https://devenv.sh/scripts/
  # scripts.hello.exec = "echo hello from $GREET";

  # https://devenv.sh/pre-commit-hooks/
  pre-commit.hooks = {
    # Nix code formatter
    alejandra.enable = true;
    # Terraform code formatter
    terraform-format.enable = true;
    # YAML linter
    yamllint.enable = true;
  };

  # https://devenv.sh/processes/
  # processes.ping.exec = "ping example.com";
}
