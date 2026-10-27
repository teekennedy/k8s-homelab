terraform {
  required_version = "~> 1.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.7"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.6"
    }
    sops = {
      source  = "carlpett/sops"
      version = "~> 1.4"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 3.0"
    }
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }
}

provider "kubernetes" {
  config_path = "../.devenv/state/kube/config"
}

provider "cloudflare" {
  email   = local.cloudflare_email
  api_key = local.cloudflare_api_key
}
