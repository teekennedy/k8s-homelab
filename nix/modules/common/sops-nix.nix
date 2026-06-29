{
  config,
  inputs,
  ...
}: {
  imports = [inputs.sops-nix.nixosModules.sops];
  config = {
    # This will automatically import SSH keys as age keys
    sops.age.sshKeyPaths = ["/persistent/etc/ssh/ssh_host_ed25519_key"];
    # Use a systemd service to install secrets on every boot, not just during activation.
    # Without this, secrets in /run/secrets.d/ (tmpfs) are lost after reboot.
    sops.useSystemdActivation = true;
    sops.secrets.default_user_hashed_password = {
      neededForUsers = true;
    };
    users.users."${config.defaultUsername}".hashedPasswordFile = config.sops.secrets.default_user_hashed_password.path;
  };
}
