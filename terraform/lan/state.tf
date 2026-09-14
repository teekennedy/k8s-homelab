# Supplied from tfvars.sops.yaml via the environment:
#   TF_VAR_state_passphrase="$(sops decrypt tfvars.sops.yaml | yq -r .tofu_state_passphrase)"
#
# The encryption block is evaluated statically, before any provider starts, so
# it cannot read data.sops_file.tfvars the way the rest of this module does.
variable "state_passphrase" {
  type      = string
  sensitive = true
}

terraform {
  encryption {
    key_provider "pbkdf2" "state_key_provider" {
      passphrase = var.state_passphrase
    }

    method "aes_gcm" "state_encryption_method" {
      keys = key_provider.pbkdf2.state_key_provider
    }

    # This module's state includes sensitive data, so encryption is enforced.
    state {
      method = method.aes_gcm.state_encryption_method

      enforced = true
    }
  }
}
