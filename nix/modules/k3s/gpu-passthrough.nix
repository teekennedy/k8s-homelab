{
  config,
  lib,
  pkgs,
  ...
}:
lib.mkIf
(
  # facter detected graphics
  builtins.hasAttr "hardware" config.facter.report && (builtins.elemAt config.facter.report.hardware.graphics_card 0).vendor.name == "nVidia Corporation"
)
{
  environment.systemPackages = [
    pkgs.nvidia-vaapi-driver
    # TODO integrate this with prometheus
    # pkgs.prometheus-nvidia-gpu-exporter
  ];
  hardware = {
    nvidia-container-toolkit.enable = true;
    # needed to install nvidia drivers for nvidia-container-toolkit
    graphics.enable = true;
    # use proprietary driver
    nvidia.open = lib.mkDefault (! config.nixpkgs.config.allowUnfree);
  };
  services.xserver.videoDrivers = ["nvidia"];

  nixpkgs.config.nvidia.acceptLicense = true;
}
