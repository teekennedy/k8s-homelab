terraform {
  backend "s3" {
    bucket = "${var.state_bucket_name}-${var.aws_region}"
    key    = "${var.environment}/terraform.tfstate"
    region = var.aws_region

    # Server-side encryption at rest.
    # State is also encrypted locally before pushing to S3.
    encrypt = true

    # DynamoDB is the long-standing lock mechanism; use_lockfile adds S3
    # conditional-write locking alongside it. Both are required for apply.
    dynamodb_table = "${var.state_bucket_name}-${var.aws_region}"
    use_lockfile   = true
  }
}
