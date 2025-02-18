{pkgs, ...}: {
  environment.systemPackages = with pkgs; [
    btop
    dig
    file
    git
    jq
    neovim
    tree
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
