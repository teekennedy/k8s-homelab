# Nix store related options
{
  config,
  inputs,
  lib,
  ...
}: {
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 14d";
    randomizedDelaySec = "45min";
  };
}
