data "sops_file" "tfvars" {
  source_file = "tfvars.sops.yaml"
}

locals {
  unifi_api_key        = coalesce(var.unifi_api_key, data.sops_file.tfvars.data["unifi_api_key"])
  unifi_api_url        = coalesce(var.unifi_api_url, data.sops_file.tfvars.data["unifi_api_url"])
  unifi_allow_insecure = coalesce(var.unifi_allow_insecure, data.sops_file.tfvars.data["unifi_allow_insecure"])
  unifi_site           = coalesce(var.unifi_site, data.sops_file.tfvars.data["unifi_site"])
  inventory            = try(yamldecode(file("${path.module}/inventory.yaml")), {})
}
