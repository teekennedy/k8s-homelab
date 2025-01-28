{
  lib,
  config,
  pkgs,
  ...
}: {
  options = {
    # SSH authorizedKeyFiles for the default user login (tkennedy, nixos)
    defaultUsername = lib.mkOption {
      type = lib.types.str;
      description = "Username of the default login / ssh user";
    };
    defaultUserAuthorizedKeyFiles = lib.mkOption {
      type = lib.types.listOf lib.types.path;
      description = "SSH authorizedKeyFiles for the default user login";
      default = builtins.map (s: ./authorized_keys + "/${s}") (builtins.attrNames (builtins.readDir ./authorized_keys));
    };
  };
  config = {
    # Don't allow user password to be changed interactively
    users.mutableUsers = false;

    # Disable sudo prompt for `wheel` users.
    security.sudo.wheelNeedsPassword = lib.mkDefault false;
    # Don't bother with the lecture or the need to keep state about who's been lectured
    security.sudo.extraConfig = "Defaults lecture=\"never\"";

    # default user
    users.users."${config.defaultUsername}" = {
      isNormalUser = true;
      openssh.authorizedKeys.keyFiles = config.defaultUserAuthorizedKeyFiles;
      extraGroups = ["wheel"]; # Enable ‘sudo’ for the user.
      packages = with pkgs; [
        file
        btop
        kubectl
        tree
        neovim
        lnav # logfile navigator
      ];
    };
  };
}
