{config, ...}: {
  imports = [
    ../common/laptop.nix
  ];
  # borg-1 has an nVidia 750m which is only supported by the legacy 470.xx driver
  hardware.nvidia.package = config.boot.kernelPackages.nvidiaPackages.legacy_470;
}
