module "cloudflare" {
  source                = "./cloudflare"
  cloudflare_domain     = local.cloudflare_domain
  cloudflare_email      = local.cloudflare_email
  cloudflare_api_key    = local.cloudflare_api_key
  cloudflare_account_id = local.cloudflare_account_id
  k8s_hosts = {
    "borg-0" = { ipv4 = "10.69.80.10" }
    "borg-1" = { ipv4 = "10.69.80.11" }
    "borg-2" = { ipv4 = "10.69.80.12" }
  }
}

module "ntfy" {
  source = "./ntfy"
  auth   = yamldecode(data.sops_file.tfvars.raw).ntfy
}

module "extra_secrets" {
  for_each  = local.extra_secrets
  source    = "./k8s-secret"
  name      = each.key
  namespace = each.value.namespace
  data      = each.value.data
}
