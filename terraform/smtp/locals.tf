locals {
  account_id       = data.aws_caller_identity.this.account_id
  region           = data.aws_region.this.name
  ses_identity_arn = "arn:${data.aws_partition.this}:ses:${local.region}:${local.account_id}:identity/${var.domain}"
}
