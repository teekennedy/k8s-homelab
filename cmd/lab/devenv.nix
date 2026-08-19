{pkgs, ...}: let
  lab = pkgs.callPackage ./default.nix {};
in {
  packages = [lab];

  # Requires devenv-zsh plugin to be imported alongside this module
  zsh.extraInit = ''
    export FPATH="''${FPATH-}:${lab}/share/zsh/site-functions"
    autoload -Uz _lab
    compdef _lab lab
  '';
}
