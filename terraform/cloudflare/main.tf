data "cloudflare_zone" "zone" {
  name = var.cloudflare_domain
}

data "cloudflare_api_token_permission_groups" "all" {}

resource "random_password" "tunnel_secret" {
  length  = 64
  special = false
}

resource "cloudflare_zero_trust_tunnel_cloudflared" "homelab" {
  account_id = var.cloudflare_account_id
  name       = "homelab"
  secret     = random_password.tunnel_secret.result
}

# Not proxied, not accessible. Just a record for auto-created CNAMEs by external-dns.
resource "cloudflare_record" "tunnel" {
  zone_id = data.cloudflare_zone.zone.id
  type    = "CNAME"
  name    = "homelab-tunnel"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.homelab.id}.cfargotunnel.com"
  proxied = false
  ttl     = 1 # Auto
}

module "cloudflared_credentials_secret" {
  source    = "../k8s-secret"
  name      = "cloudflared-credentials"
  namespace = "cloudflared"

  data = {
    "credentials.json" = jsonencode({
      AccountTag   = var.cloudflare_account_id
      TunnelName   = cloudflare_zero_trust_tunnel_cloudflared.homelab.name
      TunnelID     = cloudflare_zero_trust_tunnel_cloudflared.homelab.id
      TunnelSecret = random_password.tunnel_secret.result
    })
  }
}

resource "cloudflare_api_token" "external_dns" {
  name = "homelab_external_dns"

  policy {
    permission_groups = [
      data.cloudflare_api_token_permission_groups.all.zone["Zone Read"],
      data.cloudflare_api_token_permission_groups.all.zone["DNS Write"]
    ]
    resources = {
      "com.cloudflare.api.account.zone.*" = "*"
    }
  }
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

  policy {
    permission_groups = [
      data.cloudflare_api_token_permission_groups.all.zone["Zone Read"],
      data.cloudflare_api_token_permission_groups.all.zone["DNS Write"]
    ]
    resources = {
      "com.cloudflare.api.account.zone.*" = "*"
    }
  }
}

module "cloudflare_api_token_secret" {
  source    = "../k8s-secret"
  name      = "cloudflare-api-token"
  namespace = "cert-manager"


  data = {
    "api-token" = cloudflare_api_token.cert_manager.value
  }
}
