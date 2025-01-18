{
  pkgs,
  config,
  lib,
  ...
}: {
  # Don't allow user password to be changed interactively
  users.mutableUsers = false;
  # Disable sudo prompt for `wheel` users.
  security.sudo.wheelNeedsPassword = lib.mkDefault false;

  # Add wheel group to nix trusted users
  nix.trustedUsers = ["root" "@wheel"];

  # Password is unique per host with secret stored in hosts/<host>/secrets.yaml
  sops.secrets.tkennedy_hashed_password = {
    neededForUsers = true;
  };

  # tkennedy user
  users.users.tkennedy = {
    isNormalUser = true;
    openssh.authorizedKeys.keys = [
      "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
      # GPG SSH key
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
    ];
    extraGroups = ["wheel"]; # Enable ‘sudo’ for the user.
    packages = with pkgs; [
      file
      htop-vim
      kubectl
      tree
      vim
    ];
    hashedPasswordFile = config.sops.secrets.tkennedy_hashed_password.path;
  };
}
