{config, ...}: {
  system.activationScripts.show-update-changelog = ''
    if [[ -e /run/current-system ]]; then
      echo "Changes since last activation:"
      ${config.nix.package}/bin/nix run nixpkgs#nvd -- diff /run/current-system "$systemConfig" || true
    fi
  '';
}
