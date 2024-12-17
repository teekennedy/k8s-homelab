{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    file
    btop
    tree
    neovim
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
