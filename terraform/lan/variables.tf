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

variable "aws_region" {
  type        = string
  description = "AWS region holding the remote state bucket and lock table."
  default     = "us-west-2"
}

variable "environment" {
  type        = string
  description = "Environment name. Also the first path segment of the state key."
  default     = "k8s-homelab"
}

variable "state_bucket_name" {
  type        = string
  description = "Base name of the remote state bucket and lock table. The region is appended automatically."
  default     = "missingtoken-terraform-state"
}
