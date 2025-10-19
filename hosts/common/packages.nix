{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    dig
    file
    git
    jq
    neovim
    ripgrep
    tree
    # diagnostic tools
    btop
    pciutils # for lspci
    smartmontools # for smartctl
    lsof
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
