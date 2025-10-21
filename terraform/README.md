# Homelab terraform

These modules contain everything externally needed to setup the k8s cluster. It uses OpenTofu for applying state.

The state is encrypted with a password using the `state_passphrase` variable. This and other variables such as CloudFlare / AWS credentials are stored in the terraform.tfstate file, which is loaded into opentofu using the terraform-sops-provider.

To bootstrap, copy tfvars.sops.example.yaml to tfvars.sops.yaml and then run `sops edit tfvars.sops.yaml` to fill in the values.

To plan / apply, run `TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_local_state_passphrase')" tofu <action>`. This passes the tofu state passphrase from the encrypted tfvars.sops.yaml into opentofu so it can read / write the encrypted state.

## Getting restic-backup-user creds

After applying the backup module, use the following commands to get the access keys for restic-backup-user:

```
# access_key_id
TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_local_state_passphrase')" tofu output -show-sensitive -json | jq -r .backup_access_key_id.value

# secret_access_key
TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_local_state_passphrase')" tofu output -show-sensitive -json | jq -r .backup_encrypted_secret_access_key.value | base64 -d | gpg --decrypt
```

Save these values to modules/restic/secrets.enc.yaml as environment variables under the `restic_env_file` yaml key.
