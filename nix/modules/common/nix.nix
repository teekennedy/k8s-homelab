# Nix settings
{...}: {
  nix.gc = {
    automatic = true;
    dates = "weekly";
    options = "--delete-older-than 14d";
    randomizedDelaySec = "45min";
  };
  nix.settings = {
    extra-substituters = [
      "https://nix-community.cachix.org"
    ];

    experimental-features = ["nix-command" "flakes"];

    extra-trusted-public-keys = [
      "nix-community.cachix.org-1:mB9FSh9qf2dCimDSUo8Zy7bkq5CX+/rkCWyvRCYg3Fs="
    ];
  };
}
