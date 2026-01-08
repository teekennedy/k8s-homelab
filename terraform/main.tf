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
    "borg-3" = { ipv4 = "10.69.80.13" }
  }
}

module "smtp" {
  source   = "./smtp"
  domain   = local.cloudflare_domain
  username = join("-", [replace(local.cloudflare_domain, ".", "-"), "smtp-user"])
  pgp_key  = local.pgp_key
}

module "smtp_secret" {
  source    = "./k8s-secret"
  name      = "smtp-creds"
  namespace = "auth-system"
  data = {
    username = module.smtp.smtp_access_key_id
    password = module.smtp.smtp_secret_access_key
  }
}

module "ntfy" {
  source = "./ntfy"
  auth   = yamldecode(data.sops_file.tfvars.raw).ntfy
}

module "backup" {
  source             = "./backup"
  backup_bucket_name = local.backup_bucket_name
  environment        = local.environment
  backup_users = {
    "restic-backup-user" = {
      s3_prefix = "restic/"
    }
    "longhorn-backup-user" = {
      s3_prefix = "longhorn/"
    }
  }
}

module "longhorn_backup_secret" {
  source    = "./k8s-secret"
  name      = "longhorn-backup-user"
  namespace = "longhorn-system"
  data      = sensitive(module.backup.access_keys["longhorn-backup-user"])
}

module "extra_secrets" {
  # for_each values _must_ be nonsensitive
  for_each  = nonsensitive(local.extra_secrets)
  source    = "./k8s-secret"
  name      = each.key
  namespace = each.value.namespace
  data      = sensitive(each.value.data)
}
