variable "state_passphrase" {
  type      = string
  sensitive = true
}

terraform {
  encryption {
    key_provider "pbkdf2" "local_state_key_provider" {
      passphrase = var.state_passphrase
    }

    method "aes_gcm" "local_state_encryption_method" {
      keys = key_provider.pbkdf2.local_state_key_provider
    }

    state {
      method = method.aes_gcm.local_state_encryption_method

      enforced = true
    }
  }
}

