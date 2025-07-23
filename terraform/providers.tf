terraform {
  required_version = "~> 1.8"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.52"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.5.0"
    }
    sops = {
      source  = "carlpett/sops"
      version = "~> 1.2.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.38.0"
    }
  }
}

provider "kubernetes" {
  config_path = "../.devenv/state/kube/config"
}
