variable "backup_bucket_name" {
  type        = string
  description = "Name for the backup S3 bucket. The region will be appended automatically."
}

variable "backup_users" {
  description = "Backup IAM users and their S3 permissions."
  type = map(
    object({
      s3_prefix = string
      path      = optional(string)
    })
  )
  default = {
    "restic-backup-user" = {
      s3_prefix = "restic/"
    }
  }

  validation {
    condition     = length(var.backup_users) > 0
    error_message = "backup_users must contain at least one entry."
  }
}

variable "aws_region" {
  type        = string
  description = "AWS region to deploy resources in"
  default     = "us-west-2"
}

variable "environment" {
  type        = string
  description = "Environment name for tagging resources"
  default     = "k8s-homelab"
}
