# Personal Kubernetes Homelab

This is the repo I use to manage the bare metal k8s cluster I setup at home out of some old laptops that were donated by friends and family.

I started with [onedr0p's k3s template](https://github.com/onedr0p/flux-cluster-template) and customized accordingly.

# Setup

## Bootstrapping Secrets

This repo uses [sops-nix](https://github.com/Mic92/sops-nix) to manage sensitive data. There are many ways to encrypt secrets in sops-nix, but I'm only going to go over using a Yubikey-backed ed25519 GPG key.

This is a simple set of instructions and does not cover alternative options or tradeoffs. For a more in depth guide, refer to [MuSigma's guide](https://musigma.blog/2021/05/09/gpg-ssh-ed25519.html) or [drduh's guide](https://github.com/drduh/YubiKey-Guide?tab=readme-ov-file#prepare-gnupg)

### GPG setup

Install gnupg if you haven't already:

```sh
brew install gnupg pinentry-mac
```

Grab the hardened gpg and gpg-agent configs from [drduh's config repo](https://github.com/drduh/config):

```sh
cd
mkdir -p .gnupg
cd ~/.gnupg
curl -fSsLO https://raw.githubusercontent.com/drduh/config/master/gpg.conf
curl -fSsLO https://raw.githubusercontent.com/drduh/config/master/gpg-agent.conf
```

Uncomment the `pinentry-program` line that corresponds to your environment.

Generate your certification master key. You will be prompted for a passphrase:

```sh
gpg --quick-generate-key \
    'Your Name <your.email@example.com>' \
    ed25519 cert never
```

Export variables for the GPG key fingerprint (without spaces) and the desired expiration duration, then generate subkeys:

```sh
export KEYFP=1234567890...
export EXPIRATION=2y
gpg --quick-add-key $KEYFP ed25519 sign $EXPIRATION
gpg --quick-add-key $KEYFP cv25519 encr $EXPIRATION
gpg --quick-add-key $KEYFP ed25519 auth $EXPIRATION
```

Verify keys have been added:

```sh
gpg -K
```

Example output:

```
sec   ed25519/0x1111111111111111 2024-07-25 [C]
      Key fingerprint = 1111 1111 1111 1111 1111  1111 1111 1111 1111 1111
uid                   [ultimate] Terrance Kennedy <terrance@missingtoken.net>
ssb   ed25519/0x1111111111111111 2024-07-25 [S] [expires: 2026-07-25]
ssb   cv25519/0x1111111111111111 2024-07-25 [E] [expires: 2026-07-25]
ssb   ed25519/0x1111111111111111 2024-07-25 [A] [expires: 2026-07-25]
```

Export / backup keys somewhere secure:

```sh
export EXPORT_DIR="$(mktemp -d)"
gpg --output $EXPORT_DIR/$KEYID-Certify.key \
    --batch --pinentry-mode=loopback \
    --armor --export-secret-keys $KEYID

gpg --output $EXPORT_DIR/$KEYID-Subkeys.key \
    --batch --pinentry-mode=loopback \
    --armor --export-secret-subkeys $KEYID

gpg --output $EXPORT_DIR/$KEYID-$(date +%F).asc \
    --armor --export $KEYID
```

### Yubikey setup

Set environment variables that will be used:

```sh
export USER_PIN="xxxxxx"
export ADMIN_PIN="yyyyyyyy"
```

Change the Admin PIN:

```console
gpg --command-fd=0 --pinentry-mode=loopback --change-pin <<EOF
3
12345678
$ADMIN_PIN
$ADMIN_PIN
q
EOF
```

Change the User PIN:

```console
gpg --command-fd=0 --pinentry-mode=loopback --change-pin <<EOF
1
123456
$USER_PIN
$USER_PIN
q
EOF
```

Remove and re-insert YubiKey.

**Warning** Three incorrect _User PIN_ entries will cause it to become blocked and must be unblocked with either the _Admin PIN_ or _Reset Code_. Three incorrect _Admin PIN_ or _Reset Code_ entries will destroy data on YubiKey.

The number of [retry attempts](https://docs.yubico.com/software/yubikey/tools/ykman/OpenPGP_Commands.html#ykman-openpgp-access-set-retries-options-pin-retries-reset-code-retries-admin-pin-retries) can be changed, for example to 5 attempts:

```console
ykman openpgp access set-retries 5 5 5 -f -a $ADMIN_PIN
```

Transfer subkeys to Yubikey. Note that this is a one way operation that replaces the private key files with a stub. Make sure you've backed up first.

```sh

```

Set the touch policy for all keys to 15 second cached:

```sh
for keytype in sig dec aut att; do
  ykman openpgp keys set-touch "$keytype" cached --force --admin-pin "$ADMIN_PIN"
done
```

If you need to import the keys to another yubikey, you'll first have to delete the existing private key stubs and re-import from backup:

```console
pushd ~/.gnupg/private-keys-v1.d
rm -f $(gpg --with-colons --list-secret-keys $KEYID | awk -F: '/^grp/ {print $10  ".key"}')
popd
```

### Bootstrapping the host keys

For each host managed by colmena, we're going to use its ssh key to decrypt secrets.
Sops does not support ssh directly, so the keys are converted to age keys:

`nix-shell -p ssh-to-age --run 'ssh-keyscan <host> | grep ssh-ed25519 | ssh-to-age'`

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

## Bootstrapping k3s cluster

Start your first k3s server with `services.k3s.clusterInit = true;`. See modules/k3s/k3s.nix for a full example.

Once applied, log into the host and check that k3s is running with `sudo systemctl status k3s.service`. Check the k3s logs for errors as well `sudo journalctl -fu k3s.service`.

Get the contents of the k3s token and kubernetes config secrets at /var/lib/rancher/k3s/server/token and /etc/rancher/k3s/k3s.yaml respectively, and save them under modules/k3s/kubeconfig.enc.yaml and hosts/<hostname>/secrets.yaml:k3s_token respectively.
