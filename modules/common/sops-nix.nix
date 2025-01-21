{
  config,
  inputs,
  lib,
  ...
}: {
  imports = [inputs.sops-nix.nixosModules.sops];
  config = {
    # This will automatically import SSH keys as age keys
    sops.age.sshKeyPaths = ["/persistent/etc/ssh/ssh_host_ed25519_key"];
    sops.secrets.default_user_hashed_password = {
      neededForUsers = true;
    };
    users.users."${config.defaultUsername}".hashedPasswordFile = config.sops.secrets.default_user_hashed_password.path;
  };
}
