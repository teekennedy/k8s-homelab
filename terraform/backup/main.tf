data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  bucket_name = "${var.backup_bucket_name}-${data.aws_region.current.region}"
}

#tfsec:ignore:aws-s3-enable-bucket-logging
resource "aws_s3_bucket" "backup" {
  bucket = local.bucket_name

  tags = {
    Name    = "${var.environment} Backup Bucket"
    Purpose = "Backups"
  }
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
      aws_s3_bucket.backup.arn,
      "${aws_s3_bucket.backup.arn}/*"
    ]
    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "backup" {
  bucket = aws_s3_bucket.backup.id
  policy = data.aws_iam_policy_document.bucket_policy.json
}

resource "aws_s3_bucket_ownership_controls" "backup" {
  bucket = aws_s3_bucket.backup.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

# encryption.tf
resource "aws_s3_bucket_server_side_encryption_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.backup.arn
    }
    # S3 Bucket Keys reduce KMS request costs for SSE-KMS
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "backup" {
  bucket = aws_s3_bucket.backup.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

#  Lifecycle config for backup-friendly storage classes
resource "aws_s3_bucket_lifecycle_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id

  # Rule 1: Large objects (>=128 KB): Standard -> IA (45d) -> Glacier Instant Retrieval (90d)
  rule {
    id     = "backup-large-objects-to-ia-then-gir"
    status = "Enabled"

    filter {
      object_size_greater_than = 131072 # 128 KB
    }

    transition {
      days          = 45
      storage_class = "STANDARD_IA"
    }

    transition {
      days          = 90
      storage_class = "GLACIER_IR"
    }
  }

  # Rule 2: Small objects (<128 KB): Standard -> IA (45d), do NOT move to GIR
  rule {
    id     = "backup-small-objects-to-ia-only"
    status = "Enabled"

    filter {
      object_size_less_than = 131072 # 128 KB
    }

    transition {
      days          = 45
      storage_class = "STANDARD_IA"
    }
  }

  # Rule 3: Housekeeping — abort incomplete multipart uploads
  rule {
    id     = "abort-incomplete-mpu-7d"
    status = "Enabled"

    # Apply to whole bucket, no prefix
    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}
