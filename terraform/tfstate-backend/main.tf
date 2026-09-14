data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  # The region is resolved from the configured provider rather than var.aws_region
  # so the bucket name can never drift from the region it actually lives in.
  name = "${var.state_bucket_name}-${data.aws_region.current.region}"
}

# Access logging is disabled: it would need a second bucket, and CloudTrail data
# events already cover "who read state" for the rare times that is asked. OpenTofu's
# pbkdf2 state encryption also means a reader of the raw objects sees ciphertext.
#tfsec:ignore:aws-s3-enable-bucket-logging trivy:ignore:AWS-0089
resource "aws_s3_bucket" "state" {
  bucket = local.name

  # This bucket holds the only copy of every module's state. Losing it is not
  # recoverable by re-applying, so refuse to plan its destruction at all.
  lifecycle {
    prevent_destroy = true
  }

  tags = {
    Name    = "${var.environment} OpenTofu State"
    Purpose = "OpenTofu remote state"
  }
}

# Versioning is what makes a corrupted or truncated apply recoverable -- roll the
# object back to the previous version rather than rebuilding state by hand.
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id

  versioning_configuration {
    status = "Enabled"
  }
}

# SSE-S3 rather than SSE-KMS: with the AWS managed aws/s3 key the key policy
# grants the whole account anyway, so KMS would add per-request charges on every
# state read and write without narrowing access. OpenTofu's own pbkdf2 state
# encryption is the layer that actually keeps secrets in state unreadable.
#tfsec:ignore:aws-s3-encryption-customer-key trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket = aws_s3_bucket.state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

data "aws_iam_policy_document" "bucket_policy" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"
    principals {
      type        = "*"
      identifiers = ["*"]
    }
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.state.arn,
      "${aws_s3_bucket.state.arn}/*"
    ]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  # Deliberately no "deny unencrypted uploads" statement: default encryption
  # above already applies AES256 to every object regardless of request headers,
  # so such a statement adds nothing but a way to lock ourselves out of writing
  # state if a future backend stops sending the header.
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = data.aws_iam_policy_document.bucket_policy.json
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id

  # Versioning is unbounded by default and every apply writes a new version.
  rule {
    id     = "expire-noncurrent-state-versions"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_version_retention_days
    }
  }

  rule {
    id     = "abort-incomplete-mpu-7d"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.state]
}

resource "aws_dynamodb_table" "locks" {
  name         = local.name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  # Encryption with the AWS managed alias/aws/dynamodb key. Without this block
  # the table is still encrypted, but with an AWS *owned* key that is invisible
  # in KMS and cannot be audited.
  #tfsec:ignore:aws-dynamodb-table-customer-key trivy:ignore:AWS-0025
  server_side_encryption {
    enabled = true
  }

  point_in_time_recovery {
    enabled = true
  }

  deletion_protection_enabled = true

  lifecycle {
    prevent_destroy = true
  }

  tags = {
    Name    = "${var.environment} OpenTofu State Locks"
    Purpose = "OpenTofu state locking"
  }
}
