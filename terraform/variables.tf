variable "cloudflare_domain" {
  type        = string
  description = "CloudFlare domain name you want to use for k8s resources"
  default     = ""
}

variable "cloudflare_email" {
  type        = string
  description = "CloudFlare profile email address. Found under https://dash.cloudflare.com/profile"
  default     = ""
}

variable "cloudflare_api_key" {
  type        = string
  description = "CloudFlare Global API key. Found under https://dash.cloudflare.com/profile/api-tokens"
  default     = ""
  sensitive   = true
}

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID. https://developers.cloudflare.com/fundamentals/setup/find-account-and-zone-ids"
  default     = ""
}

variable "pgp_key" {
  type        = string
  description = "PGP key reference for IAM users"
  default     = ""
}

variable "backup_bucket_name" {
  type        = string
  description = "Name for the backup S3 bucket. The region will be appended automatically."
  default     = "missingtoken-backup"
}

variable "environment" {
  type        = string
  description = "Environment name for tagging resources"
  default     = "k8s-homelab"
}

