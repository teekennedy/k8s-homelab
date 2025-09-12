terraform {
  required_version = "~> 1.8"

  required_providers {
    sops = {
      source  = "carlpett/sops"
      version = "~> 1.2.0"
    }
    unifi = {
      source  = "ubiquiti-community/unifi"
      version = "~> 0.41"
    }
  }
}

# https://registry.terraform.io/providers/ubiquiti-community/unifi/latest/docs
provider "unifi" {
  api_key = local.unifi_api_key
  api_url = local.unifi_api_url # optionally use UNIFI_API env var

  # you may need to allow insecure TLS communications unless you have configured
  # certificates for your controller
  allow_insecure = local.unifi_allow_insecure # optionally use UNIFI_INSECURE env var

  site = local.unifi_site # optionally use UNIFI_SITE env var
}
