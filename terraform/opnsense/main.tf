terraform {
  required_version = "~> 1.8"
  required_providers {
    opnsense = {
      version = "~> 0.11"
      source  = "browningluke/opnsense"
    }
    sops = {
      source  = "carlpett/sops"
      version = "~> 1.1"
    }
  }
}

data "sops_file" "opnsense_secrets" {
  source_file = "secret.sops.yaml"
}

# https://registry.terraform.io/providers/browningluke/opnsense/latest/docs
provider "opnsense" {
  uri        = data.sops_file.opnsense_secrets.data["opnsense_uri"]
  api_key    = data.sops_file.opnsense_secrets.data["opnsense_api_key"]
  api_secret = data.sops_file.opnsense_secrets.data["opnsense_api_secret"]
}

resource "opnsense_interfaces_vlan" "vlan0800" {
  description = "k8s vlan"
  tag         = 800
  priority    = 0        # best effort (default)
  parent      = "vtnet5" # LAN
  device      = "vlan0.800"
}

resource "opnsense_interfaces_vlan" "vlan0_2" {
  description = "lan management vlan"
  tag         = 20
  priority    = 1        # background, lowest
  parent      = "vtnet5" # LAN
  device      = "vlan0.2"
}
