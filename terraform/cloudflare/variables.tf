variable "cloudflare_domain" {
  type        = string
  description = "CloudFlare domain name you want to use for k8s resources"
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

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID. https://developers.cloudflare.com/fundamentals/setup/find-account-and-zone-ids"
}

variable "k8s_hosts" {
  type = map(object({
    ipv4 = string,
  }))
  description = "Mapping of k8s hosts to IP addresses. Only IPv4 supported for now."
  default     = {}
}
