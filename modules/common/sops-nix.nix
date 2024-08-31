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
}
