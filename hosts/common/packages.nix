{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    dig
    file
    git
    jq
    neovim
    tree
    # diagnostic tools
    btop
    pciutils # for lspci
    smartmontools
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
