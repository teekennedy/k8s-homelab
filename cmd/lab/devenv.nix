{pkgs, ...}: let
  lab = pkgs.callPackage ./default.nix {};
in {
  # Interactive only. The ci profile is the container every Dagger check runs
  # in, and with the CLI in it, its closure held a Nix build of every Go file
  # here: editing one rebuilt the toolchain and re-ran every check.
  profiles.interactive.module = {
    packages = [lab];

    # Requires devenv-zsh plugin to be imported alongside this module
    zsh.extraInit = ''
      export FPATH="''${FPATH-}:${lab}/share/zsh/site-functions"
      autoload -Uz _lab
      compdef _lab lab
    '';
  };
}
