variable "aws_region" {
  type        = string
  description = "AWS region to deploy resources in"
  default     = "us-west-2"
}

variable "environment" {
  type        = string
  description = "Environment name. Also the first path segment of every state key."
  default     = "k8s-homelab"
}

variable "state_bucket_name" {
  type        = string
  description = "Base name for the state bucket and lock table. The region is appended automatically."
  default     = "missingtoken-terraform-state"
}

variable "noncurrent_version_retention_days" {
  type        = number
  description = "How long superseded state versions are kept before expiring."
  default     = 90

  validation {
    condition     = var.noncurrent_version_retention_days >= 30
    error_message = "Keep at least 30 days of state history so a bad apply stays recoverable."
  }
}
