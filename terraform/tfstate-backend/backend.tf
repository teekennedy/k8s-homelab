# Bootstrap note: this module creates the bucket and table it stores its own
# state in. On a fresh account, apply it once with this file moved aside, then
# restore it and run `tofu init -migrate-state`.
terraform {
  backend "s3" {
    bucket = "${var.state_bucket_name}-${var.aws_region}"
    key    = "${var.environment}/tfstate-backend/terraform.tfstate"
    region = var.aws_region

    encrypt = true

    # DynamoDB is the long-standing lock mechanism; use_lockfile adds S3
    # conditional-write locking alongside it. Both are required to apply.
    dynamodb_table = "${var.state_bucket_name}-${var.aws_region}"
    use_lockfile   = true
  }
}
