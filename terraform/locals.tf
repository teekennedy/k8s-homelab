data "sops_file" "tfvars" {
  source_file = "tfvars.sops.yaml"
}

locals {

  cloudflare_domain     = coalesce(var.cloudflare_domain, data.sops_file.tfvars.data["cloudflare_domain"])
  cloudflare_email      = coalesce(var.cloudflare_email, data.sops_file.tfvars.data["cloudflare_email"])
  cloudflare_api_key    = coalesce(var.cloudflare_api_key, data.sops_file.tfvars.data["cloudflare_api_key"])
  cloudflare_account_id = coalesce(var.cloudflare_account_id, data.sops_file.tfvars.data["cloudflare_account_id"])
  pgp_key               = coalesce(var.cloudflare_account_id, data.sops_file.tfvars.data["pgp_key"])
  extra_secrets         = try(yamldecode(data.sops_file.tfvars.raw).extra_secrets, {})
}
