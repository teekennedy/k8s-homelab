{
  config,
  inputs,
  lib,
  ...
}: let
  defaultSopsPath = "${toString inputs.self}/hosts/${config.networking.hostName}/secrets.yaml";
in {
  imports = [
    inputs.sops-nix.nixosModules.sops
  ];

  sops.defaultSopsFile = lib.mkIf (builtins.pathExists defaultSopsPath) defaultSopsPath;
  # This will automatically import SSH keys as age keys
  sops.age.sshKeyPaths = ["/etc/ssh/ssh_host_ed25519_key"];
  sops.secrets.ssh_host_private_key = {
    mode = "0600";
    # Either a user id or group name representation of the secret owner
    # It is recommended to get the user name from `config.users.users.<?name>.name` to avoid misconfiguration
    owner = config.users.users.root.name;
    # Either the group id or group name representation of the secret group
    # It is recommended to get the group name from `config.users.users.<?name>.group` to avoid misconfiguration
    group = config.users.users.root.group;

    reloadUnits = ["sshd.service"];

    path = "/etc/ssh/ssh_host_ed25519_key";
  };
  sops.secrets.ssh_host_public_key = {
    mode = "0644";
    # Either a user id or group name representation of the secret owner
    # It is recommended to get the user name from `config.users.users.<?name>.name` to avoid misconfiguration
    owner = config.users.users.root.name;
    # Either the group id or group name representation of the secret group
    # It is recommended to get the group name from `config.users.users.<?name>.group` to avoid misconfiguration
    group = config.users.users.root.group;

    reloadUnits = ["sshd.service"];

    path = "/etc/ssh/ssh_host_ed25519_key.pub";
  };
}
