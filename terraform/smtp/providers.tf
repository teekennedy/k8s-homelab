terraform {
  required_version = ">= 1.5.7"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.7"
    }
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3.5"
    }
  }
}

provider "aws" {
  default_tags {
    tags = {}
  }
}
