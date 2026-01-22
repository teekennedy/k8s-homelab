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
    lsof
    pciutils # for lspci
    smartmontools # for smartctl
    sysstat # for iostat
    unixtools.netstat
  ];
  hardware.enableAllFirmware = true;
  nixpkgs.config.allowUnfree = true;
}
