variable "username" {
  description = "Name of the IAM user that will hold SMTP creds as access keys"
  type        = string
}

variable "domain" {
  description = "Domain that will be used for sending email"
  type        = string
}

variable "cloudflare_email" {
  type        = string
  description = "CloudFlare profile email address. Found under https://dash.cloudflare.com/profile"
}

variable "cloudflare_api_key" {
  type        = string
  description = "CloudFlare Global API key. Found under https://dash.cloudflare.com/profile/api-tokens"
  sensitive   = true
}

variable "pgp_key" {
  type        = string
  description = "PGP key reference for the smtp IAM user"
  default     = ""
}
