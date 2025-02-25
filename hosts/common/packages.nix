{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    btop
    dig
    file
    git
    jq
    neovim
    smartmontools
    tree
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
