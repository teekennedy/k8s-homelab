variable "unifi_api_key" {
  type        = string
  description = "Unifi Network API key. See README for how to generate one."
  default     = ""
  sensitive   = true
}

variable "unifi_api_url" {
  type        = string
  description = "Unifi Network API url. Usually https://<lan IP of unifi device>."
  default     = ""
  sensitive   = true
}

variable "unifi_allow_insecure" {
  type        = bool
  description = "Whether to allow self signed certs in unifi provider connection."
  default     = true
  sensitive   = false
}

variable "unifi_site" {
  type        = string
  description = "Unifi site name to manage. Defaults to 'default'."
  default     = "default"
  sensitive   = false
}
