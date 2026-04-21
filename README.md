# Personal Kubernetes Homelab

This is the repo I use to manage the bare metal k8s cluster I setup at home out of some old laptops that were donated by friends and family.

# Setup

## Bootstrapping a NixOS Host

See [docs/nix-host-bootstrap.md](docs/nix-host-bootstrap.md) for the full guide.

Quick start:

1. Add the host to `flake.nix` in the `borgHosts` list
2. Build and boot the installer ISO: `nix build .#nixosConfigurations.installIso.config.system.build.isoImage`
3. Run: `lab host bootstrap <hostname> --ip <installer-ip>`

The bootstrap command handles secrets generation, sops configuration, hardware detection, NixOS build verification, and installation automatically.

## Deploying updates

Use deploy-rs: `deploy -- .#borg-0`

If you just want to build the nixosSystem for a host (e.g. to test configuration changes), run `nix build -L .#nixosConfigurations.borg-0.config.system.build.toplevel`. `-L` outputs build logs to stderr.

## Bootstrapping k3s cluster

Start your first k3s server with `services.k3s.clusterInit = true;`. See nix/modules/k3s/k3s.nix for a full example.

Once applied, log into the host and check that k3s is running with `sudo systemctl status k3s.service`. Check the k3s logs for errors as well `sudo journalctl -fu k3s.service`.

Run the helper script from the scripts directory to fetch all certificates, keys and tokens and write them encrypted with sops to ./nix/modules/k3s/secrets.enc.yaml

Also copy /etc/rancher/k3s/k3s.yaml from the first node and place it in ./.devenv/state/kube/config in this repo (will be gitignored). You'll need to replace `server:` with the server's actual address (not 127.0.0.1).

### Longhorn

Run `lab k8s bootstrap --env production` to install foundation components including Longhorn.

# Deployment

From the project root directory, run `colmena apply --experimental-flake-eval`.
