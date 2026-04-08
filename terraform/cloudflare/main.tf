data "cloudflare_zones" "zone" {
  name = var.cloudflare_domain
}

data "cloudflare_api_token_permission_groups_list" "all" {
  scope = "com.cloudflare.api.account.zone"
}

# static, internal only DNS records for borg hosts
resource "cloudflare_dns_record" "k8s_host_ipv4" {
  for_each = var.k8s_hosts
  zone_id  = data.cloudflare_zones.zone.result[0].id
  type     = "A"
  name     = "${each.key}.${var.cloudflare_domain}"
  content  = each.value.ipv4
  proxied  = false
  ttl      = 1 # Auto
}

resource "cloudflare_api_token" "external_dns" {
  name = "homelab_external_dns"

  policies = [{
    effect            = "allow"
    permission_groups = local.dns_edit_permission_groups
    resources = jsonencode({
      "com.cloudflare.api.account.zone.*" = "*"
    })
  }]
}

module "external_dns_secret" {
  source    = "../k8s-secret"
  name      = "cloudflare-api-token"
  namespace = "external-dns"

  data = {
    "value" = cloudflare_api_token.external_dns.value
  }
}

resource "cloudflare_api_token" "cert_manager" {
  name = "homelab_cert_manager"

  policies = [{
    effect            = "allow"
    permission_groups = local.dns_edit_permission_groups
    resources = jsonencode({
      "com.cloudflare.api.account.zone.*" = "*"
    })
  }]
}

module "cloudflare_api_token_secret" {
  source    = "../k8s-secret"
  name      = "cloudflare-api-token"
  namespace = "cert-system"


  data = {
    "api-token" = cloudflare_api_token.cert_manager.value
  }
}
