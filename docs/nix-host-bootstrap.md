# Bootstrapping a NixOS Host

This guide covers the full process of adding a new NixOS host to the homelab cluster.

## Prerequisites

1. **Define the host in `flake.nix`**: Add an entry to the `borgHosts` list with the hostname, system architecture, and any host-specific NixOS modules. You can leave `disko.devices.disk.main.device` commented out until after hardware detection.

2. **Build the installer ISO**: Build the custom NixOS installer that includes SSH access with your authorized keys:
   ```bash
   nix build .#nixosConfigurations.installIso.config.system.build.isoImage
   ```
   The resulting image will be symlinked to `./result`.

3. **Boot the target machine**: Write the installer ISO to a USB drive and boot the target machine from it. Note the IP address assigned to the machine.

4. **Verify SSH access**: Confirm you can SSH into the installer as root:
   ```bash
   ssh root@<ip-address>
   ```

## Automated Bootstrap

The `lab host bootstrap` command automates the entire bootstrapping process:

```bash
lab host bootstrap <hostname> --ip <installer-ip>
```

The command performs the following steps, each of which is idempotent (safe to re-run):

### 1. Validate flake configuration

Checks that the hostname has a corresponding entry in `flake.nix`'s `nixosConfigurations`. If not found, exits with an error telling you to add the host definition first.

### 2. Create host directory

Creates `nix/hosts/<hostname>/` if it doesn't already exist.

### 3. Generate secrets

Creates or updates `nix/hosts/<hostname>/secrets.yaml` with:
- **SSH host key pair** (`ssh_host_public_key`, `ssh_host_private_key`): Generated with `ssh-keygen -t ed25519`
- **Default user password hash** (`default_user_hashed_password`): You'll be prompted to enter a password, which is hashed with `mkpasswd --method=SHA-512`

If `secrets.yaml` already exists (encrypted or plaintext), only missing fields are generated. Existing fields are preserved.

### 4. Update `.sops.yaml` age key

Converts the host's SSH public key to an age key using `ssh-to-age` and adds it to the `keys` list in `.sops.yaml` with anchor `&host_<hostname>`. If the key already exists, it's updated if the value has changed.

### 5. Add sops creation rule

Adds a creation rule to `.sops.yaml` for `nix/hosts/<hostname>/secrets.yaml`, encrypted with:
- Your PGP key
- Your personal age key
- The host's age key

The rule is derived from existing host creation rules to ensure consistency.

### 6. Encrypt secrets

Encrypts `secrets.yaml` in place using `sops encrypt`. Skipped if already encrypted. The encrypted file and `.sops.yaml` are staged for git.

### 7. Update modules creation rule

Adds the host's age key to the `nix/modules/*/*.enc.yaml` creation rule so the host can decrypt shared secrets. All existing encrypted module files are re-encrypted with `sops updatekeys` to include the new key.

### 8. Generate hardware configuration

Runs `nixos-anywhere` with `--phases ''` (no installation) to detect hardware and generate `nix/hosts/<hostname>/facter.json` via `nixos-facter`. Skipped if `facter.json` already exists. The generated file is staged for git.

### 9. Check disko configuration

Verifies that `disko.devices.disk.main.device` is set in the host's NixOS configuration. If not configured, displays available disks from `facter.json` (using `/dev/disk/by-id/` paths) and asks you to update `flake.nix`. Re-run the bootstrap command after setting the device.

### 10. Build and verify

- Builds the full NixOS configuration with `nix build`
- Displays the configured `system.stateVersion`
- Runs `nix flake check` (you can choose to continue if it fails)

### 11. Install NixOS

After confirmation, runs `nixos-anywhere` to format disks and install NixOS on the target machine. The host will reboot into the new system automatically.

## After Bootstrap

1. **Deploy**: Once the host is up, run `lab host deploy <hostname>` to apply any pending configuration.

2. **Verify SSH**: Confirm you can log in as your default user: `ssh <hostname>`

3. **Update monitoring**: Add the new host's IP to `k8s/platform/monitoring-system/values.yaml` in the `&nodeIPs` list.

4. **DNS (optional)**: Add an external DNS entry by updating `k8s_hosts` in `terraform/main.tf`.

## Re-running Bootstrap

The bootstrap command is designed to be re-run safely at any point. Each step checks whether its work has already been completed:

- Existing secrets are preserved; only missing fields are generated
- `.sops.yaml` entries are checked before adding
- Already-encrypted files are not re-encrypted unnecessarily
- Existing `facter.json` is reused
- User confirmation is required before the destructive install step

This means you can interrupt the process (e.g., to configure `disko.devices.disk.main.device` in `flake.nix`) and resume by running the same command again.

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
