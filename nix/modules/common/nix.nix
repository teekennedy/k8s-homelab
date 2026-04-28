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

    # This setting is already set by the determinate nix module.
    # experimental-features = ["nix-command" "flakes"];
    # Use extra-experimental-features if you want to add more to the above.

    extra-trusted-public-keys = [
      "nix-community.cachix.org-1:mB9FSh9qf2dCimDSUo8Zy7bkq5CX+/rkCWyvRCYg3Fs="
    ];
  };
}
