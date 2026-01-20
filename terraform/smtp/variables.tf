variable "username" {
  description = "Name of the IAM user that will hold SMTP creds as access keys"
  type        = string
}

variable "domain" {
  description = "Domain that will be used for sending email"
  type        = string
}

variable "aws_region" {
  description = "AWS region where SES resources are managed"
  default     = "us-west-2"
  type        = string
}

variable "pgp_key" {
  type        = string
  description = "PGP key reference for the smtp IAM user"
  default     = ""
}

variable "notification_email" {
  description = "Email address for SES bounce/complaint notifications"
  type        = string
}
