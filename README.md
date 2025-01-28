# Personal Kubernetes Homelab

This is the repo I use to manage the bare metal k8s cluster I setup at home out of some old laptops that were donated by friends and family.

# Setup

## MacOS Specific

MacOS cannot natively build packages for

## Bootstrapping Secrets

This repo uses [sops-nix](https://github.com/Mic92/sops-nix) to manage sensitive data. There are many ways to encrypt secrets in sops, and many existing guides for setting up and managing those keys, so I won't repeat them here.
For a more in depth guide, refer to [MuSigma's guide](https://musigma.blog/2021/05/09/gpg-ssh-ed25519.html) or [drduh's guide](https://github.com/drduh/YubiKey-Guide?tab=readme-ov-file#prepare-gnupg)

I'm using a Yubikey-backed ed25519 GPG key to encrypt secrets on my development machine, and each host's SSH key to decrypt them on deployment.

### Sops-nix setup

Now that we have the keys, we'll add the key identifier (GPG fingerprint, age public key) to `.sops.yaml` at the root of the repo:

```yaml
---
keys:
  - &user_<username> <GPG Fingerprint>
  - &host_<host> <age public key>
```

Then we can reference those keys in the configuration for sops-managed files:

```yaml
creation_rules:
  - path_regex: hosts/<host>/secrets.ya?ml
    key_groups:
    - pgp:
      - *user_<username>
      age:
      - *host_<host>
```

## Bootstrapping a host

### Build installer

This repo's flake includes a customized installer that starts openssh and allows root login with the public keys added to ./modules/users/authorized_keys/.

Run `nix build .#nixosConfigurations.installIso.config.system.build.isoImage` to build the installer iso. The resulting package will by symlinked to ./result.

Write the image to some installation media and use it to boot the target machine.

### Perform initial installation

For each host, we're going to use its ssh key to decrypt secrets.
Sops does not support ssh directly, so the keys are converted to age keys.

I created a script to generate a new SSH key for a host encrypt it with sops, and save it under `./hosts/<host>/secrets.yaml`.

```bash
bootstrap-host <hostname> --target-host root@<installer-ip-or-hostname>
```

Note that you'll

### Adding secrets

With sops configuration in place, one can simply run `sops edit path/to/secrets.yaml`.
This will create or decrypt the file and open it in the editor defined in `$EDITOR`.

### Referencing secrets

First add the sops import and configuration to your configuration.nix:

```nix
imports = [ <sops-nix/modules/sops> ];

sops = {
  defaultSopsFile = ./path/to/secrets.yaml;
  # This will automatically import SSH keys as age keys
  age.sshKeyPaths = [ "/etc/ssh/ssh_host_ed25519_key" ];
};

```

Then you can add individual secret attributes that reference specific fields in secrets.yaml.
Sub-fields can be accessed using `/` as a separator between field names.

The following snippet has an example secret with common options documented in comments.
See the [sops-nix](https://github.com/Mic92/sops-nix) README for more examples and options.

<details>
<summary>sops.secret</summary>

```nix
sops.secrets.my_secret = {
  # The sops file can be overwritten per secret...
  # sopsFile = ./other-secrets.json;
  # The format of the sops file. Defaults to "yaml" but you can also use "json" or "binary"
  # format = "yaml"

  # Permission modes are in octal representation (same as chmod)
  # mode = "0440";
  # Either a user id or group name representation of the secret owner
  # It is recommended to get the user name from `config.users.users.<?name>.name` to avoid misconfiguration
  # owner = config.users.users.nobody.name;
  # Either the group id or group name representation of the secret group
  # It is recommended to get the group name from `config.users.users.<?name>.group` to avoid misconfiguration
  # group = config.users.users.nobody.group;

  # It is possible to restart or reload units when a secret changes or is newly initialized.
  # restartUnits = [ "home-assistant.service" ];
  # there is also `reloadUnits` which acts like a `reloadTrigger` in a NixOS systemd service

  # Users are normally setup before secrets are resolved.
  # Set this to true if the secret is needed to setup users.
  # neededForUsers = true;

  # Some services might expect files in certain locations. Using the path option a symlink to this directory can be created:
  # path = "/var/lib/hass/secrets.yaml";
};
```

</details>

These secrets will be decrypted under `/run/secrets` (or `/run/secrets-for-users` if the secret is `neededForUsers`).
You can reference the secret's path from elsewhere in the config using the `.path` attribute,
e.g. `users.users.my-user.hashedPasswordFile = config.sops.secrets.my-password.path;`.

## Setting up a new host

To setup a new host, create the following config:

- Generate ed25519 ssh key for the host:
  - `ssh-keygen -t ed25519 -C "root@$hostname" -f "$(pwd)/ssh_host_ed25519_key"`
  - save private key to hosts/<hostname>/secrets.yaml under `ssh_host_private_key`
  - save public key to hosts/<hostname>/secrets.yaml under `ssh_host_public_key`
  - convert ssh key to age with `nix-shell -p ssh-to-age --run 'ssh-to-age -i ./ssh_host_ed25519_key'` and save the public age key to .sops.yaml under `keys`.
  - run `sops updatekeys` against all current encrypted files to add the new key.
- build the nixos installer image:
  - `nix build .#nixosConfigurations.installIso.config.system.build.isoImage`
- write the installer image to a drive and boot the machine from it
- generate and save facter configuration:
  - `nixos-anywhere -- --flake .#nixosConfigurations.borg-0 --generate-hardware-config nixos-facter ./hosts/borg-0/facter.json --target-host root@borg-0`
- Set `disko.devices.disk.main.device` to the filesystem root device, and optionally `disko.longhornDevice` to the device used for k8s longhorn.
  - Make sure to use persistent device names from `/dev/disk/by-id`. Check the generated facter.json for available device names.
- bootstrap the host:
  - `bootstrap-host borg-0 -- --target-host root@<ip_addr>`
- confirm you're able to login as your default user via SSH `ssh borg-0`

## Deploying updates

Use deploy-rs: `deploy -- .#borg-0 --override-input devenv-root "file+file://"<(printf %s "$PWD")`

## Bootstrapping k3s cluster

Start your first k3s server with `services.k3s.clusterInit = true;`. See modules/k3s/k3s.nix for a full example.

Once applied, log into the host and check that k3s is running with `sudo systemctl status k3s.service`. Check the k3s logs for errors as well `sudo journalctl -fu k3s.service`.

Run the helper script from the scripts directory to fetch all certificates, keys and tokens and write them encrypted with sops to ./modules/k3s/secrets.enc.yaml

Also copy /etc/rancher/k3s/k3s.yaml from the first node and place it in ./.devenv/state/kube/config in this repo (will be gitignored). You'll need to replace `server:` with the server's actual address (not 127.0.0.1).

# Deployment

From the project root directory, run `colmena apply --experimental-flake-eval`.
