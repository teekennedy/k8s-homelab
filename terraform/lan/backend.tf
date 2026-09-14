# The s3 backend is built into OpenTofu, so this module stores state in AWS
# without taking a dependency on the AWS provider. It still needs AWS
# credentials in the environment to run.
terraform {
  backend "s3" {
    bucket = "${var.state_bucket_name}-${var.aws_region}"
    key    = "${var.environment}/lan/terraform.tfstate"
    region = var.aws_region

    encrypt = true

    # DynamoDB is the long-standing lock mechanism; use_lockfile adds S3
    # conditional-write locking alongside it. Both are required to apply.
    dynamodb_table = "${var.state_bucket_name}-${var.aws_region}"
    use_lockfile   = true
  }
}
