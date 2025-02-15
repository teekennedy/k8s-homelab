{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    file
    btop
    tree
    neovim
    git
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
