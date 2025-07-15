data "aws_region" "this" {}

data "aws_caller_identity" "this" {}

data "aws_partition" "this" {}

data "cloudflare_zone" "zone" {
  name = var.domain
}
