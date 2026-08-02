{
  pkgs,
  lib,
  ...
}: let
  lab = pkgs.buildGoModule {
    pname = "lab";
    version = "0.2.3";
    src = ./.;
    vendorHash = "sha256-dnnX0KlWNUXVHyZDORxDCsNla9EY7b417+uNTLhUQmE=";

    postInstall = ''
      installShellCompletion --cmd lab \
        --bash <($out/bin/lab completion bash) \
        --zsh <($out/bin/lab completion zsh) \
        --fish <($out/bin/lab completion fish)
    '';

    nativeBuildInputs = with pkgs; [installShellFiles];

    meta = with lib; {
      description = "Unified CLI for k8s-homelab management";
      homepage = "https://github.com/teekennedy/homelab";
      license = licenses.mit;
      maintainers = [];
      mainProgram = "lab";
    };
  };
in {
  packages = [lab];

  # Requires devenv-zsh plugin to be imported alongside this module
  zsh.extraInit = ''
    export FPATH="''${FPATH-}:${lab}/share/zsh/site-functions"
    autoload -Uz _lab
    compdef _lab lab
  '';
}
