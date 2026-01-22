{
  config,
  pkgs,
  lib,
  ...
}:
lib.mkIf (builtins.hasAttr "hardware" config.facter.report && (builtins.elemAt config.facter.report.hardware.cpu 0).vendor_name == "GenuineIntel") {
  hardware.graphics = {
    enable = true;
    extraPackages = with pkgs; [
      intel-media-driver # Intel GPU driver supporting Broadwell+ iGPUs
      intel-vaapi-driver # va-api user mode driver
    ];
  };
  environment.systemPackages = with pkgs; [
    libva-utils # optional, provides vainfo command
  ];
}
