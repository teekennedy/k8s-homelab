module "ntfy_auth_secret" {
  source    = "../k8s-secret"
  name      = "webhook-transformer"
  namespace = "monitoring-system"

  data = {
    NTFY_URL   = var.auth.url
    NTFY_TOPIC = var.auth.topic
  }
}
