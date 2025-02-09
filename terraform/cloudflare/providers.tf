terraform {
  required_version = "~> 1.8"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.52"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35.1"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6.3"
    }
  }
}

provider "cloudflare" {
  email   = var.cloudflare_email
  api_key = var.cloudflare_api_key
}

