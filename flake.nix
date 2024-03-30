{
  description = "teekennedy's K3s bare metal cluster";
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixpkgs_stable.url = "nixpkgs/nixos-23.11";
  };
  outputs = {nixpkgs, ...}: let
    inherit (nixpkgs) lib;
  in {
    colmena = {
      meta = {
        description = "K3s cluster";
        nixpkgs = import nixpkgs {
          system = "x86_64-linux";
          overlays = [];
        };
      };

      borg-0 = {
        name,
        nodes,
        pkgs,
        ...
      }: {
        deployment = {
          tags = [];
          # Copy the derivation to the target node and initiate the build there
          buildOnTarget = true;
          targetUser = null; # Defaults to $USER
          targetHost = "borg-0.local";
        };

        # copied from hardware-configuration.nix
        boot.initrd.availableKernelModules = ["xhci_pci" "ahci" "usb_storage" "usbhid" "uas" "sd_mod"];
        boot.initrd.kernelModules = ["dm-snapshot"];
        boot.kernelModules = ["kvm-intel"];
        boot.extraModulePackages = [];
        networking.useDHCP = true;

        fileSystems."/" = {
          device = "/dev/disk/by-uuid/c5de90e4-5cfb-43a1-aeed-a61c49e52881";
          fsType = "ext4";
        };

        fileSystems."/boot" = {
          device = "/dev/disk/by-uuid/CE2A-C751";
          fsType = "vfat";
        };

        fileSystems."/nix" = {
          device = "/dev/disk/by-uuid/b79a1fc6-9b6b-46ee-8d4f-f2d86e62058a";
          fsType = "ext4";
        };

        fileSystems."/home" = {
          device = "/dev/disk/by-uuid/c4bf9cdc-d3ce-4f42-b0ed-dabb5c00a767";
          fsType = "ext4";
        };

        swapDevices = [
          {device = "/dev/disk/by-uuid/6a6335d2-43bd-4229-9b57-a137d64e1053";}
        ];

        # copied from configuration.nix
        nix.settings.experimental-features = ["nix-command" "flakes"];

        # Use the systemd-boot EFI boot loader.
        boot.loader.systemd-boot.enable = true;
        boot.loader.efi.canTouchEfiVariables = true;
        # Limit the number of generations in the boot menu. Default is null which is unlimited.
        boot.loader.systemd-boot.configurationLimit = 120;

        networking.hostName = "borg-0"; # Define your hostname.

        # Set your time zone.
        time.timeZone = "America/Denver";

        # Configure network proxy if necessary
        # networking.proxy.default = "http://user:password@proxy:port/";
        # networking.proxy.noProxy = "127.0.0.1,localhost,internal.domain";

        # Define a user account. Don't forget to set a password with ‘passwd’.
        users.users.tkennedy = {
          isNormalUser = true;
          initialPassword = "ChangeMe";
          openssh.authorizedKeys.keys = [
            "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
          ];
          extraGroups = ["wheel"]; # Enable ‘sudo’ for the user.
          packages = with pkgs; [
            vim
          ];
        };
        # Disable sudo prompt for `wheel` users.
        security.sudo.wheelNeedsPassword = lib.mkDefault false;

        # List packages installed in system profile. To search, run:
        # $ nix search wget
        environment.systemPackages = with pkgs; [
          vim
        ];

        # List services that you want to enable:

        # Enable systemd-resolved
        services.resolved.enable = true;
        # Enable the OpenSSH daemon.
        services.openssh.enable = true;

        # Open ports in the firewall.
        networking.firewall.allowedTCPPorts = [
          22 # ssh
        ];
        networking.firewall.allowedUDPPorts = [
          5353 # multicast DNS
        ];

        # This option defines the first version of NixOS you have installed on this particular machine,
        # and is used to maintain compatibility with application data (e.g. databases) created on older NixOS versions.
        #
        # Most users should NEVER change this value after the initial install, for any reason,
        # even if you've upgraded your system to a new NixOS release.
        #
        # This value does NOT affect the Nixpkgs version your packages and OS are pulled from,
        # so changing it will NOT upgrade your system.
        #
        # This value being lower than the current NixOS release does NOT mean your system is
        # out of date, out of support, or vulnerable.
        #
        # Do NOT change this value unless you have manually inspected all the changes it would make to your configuration,
        # and migrated your data accordingly.
        #
        # For more information, see `man configuration.nix` or https://nixos.org/manual/nixos/stable/options#opt-system.stateVersion .
        system.stateVersion = "23.11"; # Did you read the comment?
      };
    };
  };
}
