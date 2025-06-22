variable "username" {
  description = "Name of the IAM user that will hold SMTP creds as access keys"
  type        = string
}

variable "domain" {
  description = "Domain that will be used for sending email"
  type        = string
}
