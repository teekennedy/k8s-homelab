variable "backup_bucket_name" {
  type        = string
  description = "Name for the backup S3 bucket. The region will be appended automatically."
}

variable "restic_prefix" {
  description = "Optional prefix for your restic repo within the bucket (e.g., \"restic/\"). Use empty string to apply rules to the whole bucket."
  type        = string
  default     = "restic/"
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

variable "pgp_key" {
  type        = string
  description = "PGP key for encrypting IAM access key secrets"
}
