# Bootstrapping a NixOS Host

This guide covers the full process of adding a new NixOS host to the homelab cluster.

## Prerequisites

1. **Define the host in `flake.nix`**: Add an entry to the `borgHosts` list with the hostname, system architecture, and any host-specific NixOS modules. You can leave `disko.devices.disk.main.device` commented out until after hardware detection.

2. **Build the installer ISO**: Build the custom NixOS installer that includes SSH access with your authorized keys:
   ```bash
   nix build .#nixosConfigurations.installIso.config.system.build.isoImage
   ```

   If host is not the same Nix system as the installIso system, you can use the `--builders` flag to build on a remote system that matches the target system., e.g. `--builders 'ssh://borg-0 x86_64-linux'`.

   The resulting image will be symlinked to `./result`.

3. **Boot the target machine**: Write the installer ISO to a USB drive and boot the target machine from it. Note the IP address assigned to the machine.

4. **Verify SSH access**: Confirm you can SSH into the installer as root:
   ```bash
   ssh root@<ip-address>
   ```

## Bootstrap

Bootstrapping is two calls to the `nix` Dagger module, run from the repo root. They reach the installer and the hosts through the module's SSH settings (`sshAuthSock`, `sshConfig`, `sshUser`); see [.dagger/docs/nix-builders.md](../.dagger/docs/nix-builders.md#on-a-workstation).

Both take the age key sops decrypts with. It must be one of the common keys `.sops.yaml` encrypts host and module secrets to. sops encrypts to the PGP keys in `.sops/pgp/`, so add a public key there when adding one to `.sops.yaml`.

### 1. Generate the host's files

```bash
HOST_PASSWORD=... dagger api call nix host --name=<hostname> bootstrap \
  --ip=<installer-ip> \
  --sops-age-key=file:$HOME/.config/sops/age/keys.txt \
  --password=env:HOST_PASSWORD \
  --auto-apply
```

This returns a changeset; `--auto-apply` writes it to the working tree. Nothing on the target is changed. Each step keeps what the repo already has, so running it for a configured host changes nothing.

1. **Validate**: checks that the host is in the flake's `nixosConfigurations`.
2. **Host directory**: creates `nix/hosts/<hostname>/default.nix` if missing.
3. **Secrets**: fills in whatever `nix/hosts/<hostname>/secrets.yaml` lacks and writes it encrypted:
   - `ssh_host_public_key`, `ssh_host_private_key`: an ed25519 SSH host key pair
   - `default_user_hashed_password`: `--password`, hashed with `mkpasswd --method=SHA-512`. `--password` is only needed when the hash is missing.
   - `restic_repo_password`: a random password

   The plaintext only exists on a tmpfs inside the container.
4. **`.sops.yaml`**: adds the host's age key (its SSH public key through `ssh-to-age`) as `&host_<hostname>`, a creation rule for its `secrets.yaml`, and the key to the `nix/modules` rule. The module secrets are then re-keyed with `sops updatekeys`.
5. **Hardware report**: runs `nixos-anywhere --phases ''` against the installer to generate `nix/hosts/<hostname>/facter.json` with `nixos-facter`. Skipped when `facter.json` exists.
6. **Disk check and build**: if `disko.devices.disk.main.device` is not set, it lists the `/dev/disk/by-id/` paths from `facter.json` as a warning. Apply the changes, set the device in `flake.nix`, and run bootstrap again. Once it is set, bootstrap builds the host's system.

Commit the changes once the build succeeds.

### 2. Install NixOS

```bash
dagger api call nix host --name=<hostname> install \
  --ip=<installer-ip> \
  --sops-age-key=file:$HOME/.config/sops/age/keys.txt
```

**This erases the target's disks.** It decrypts the host's SSH key from `secrets.yaml` and runs `nixos-anywhere` with the key as an extra file at `/persistent/etc/ssh/`, so the installed system can decrypt its secrets. The host reboots into NixOS.

## After Bootstrap

1. **Deploy**: hosts deploy from `main` automatically ([nixos-cd.md](nixos-cd.md)). Until the host is part of that rollout, deploy it directly: `deploy --targets .#<hostname>`.

2. **Verify SSH**: Confirm you can log in as your default user: `ssh <hostname>`

3. **Update monitoring**: Add the new host's IP to `k8s/platform/monitoring-system/values.yaml` in the `&nodeIPs` list.

4. **DNS (optional)**: Add an external DNS entry by updating `k8s_hosts` in `terraform/main.tf`.

## Secrets Management

This repo uses [sops-nix](https://github.com/Mic92/sops-nix) with a Yubikey-backed GPG key for encryption on the development machine and each host's SSH key (converted to age) for decryption on deployment.

### Adding secrets

Edit encrypted secrets with: `sops edit path/to/secrets.yaml`

### Referencing secrets in NixOS

```nix
sops.secrets.my_secret = {
  # Optional: override the sops file for this secret
  # sopsFile = ./other-secrets.json;
  # Permission modes (octal)
  # mode = "0440";
  # Restart services when secret changes
  # restartUnits = [ "my-service.service" ];
};
```

Secrets are decrypted to `/run/secrets/` at deployment time. Reference paths with `config.sops.secrets.<name>.path`.
