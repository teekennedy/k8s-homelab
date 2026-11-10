data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  bucket_name = "${var.backup_bucket_name}-${data.aws_region.current.region}"
}

# Backup software (e.g. restic) is expected to manage multiple snapshot / restore points on its own.
# Therefore, versioning is purposely disabled as it wastes S3 bucket size.
# Bucket logging is disabled: restic encrypts backups client-side before upload,
# so a principal who can read this bucket only sees ciphertext.
#tfsec:ignore:aws-s3-enable-bucket-logging tfsec:ignore:aws-s3-enable-versioning trivy:ignore:AWS-0089 trivy:ignore:AWS-0090
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

  statement {
    sid    = "AllowStorageClassAnalysisExport"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
    actions = ["s3:PutObject"]
    resources = [
      "${aws_s3_bucket.backup.arn}/analytics/storage-class/*"
    ]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [aws_s3_bucket.backup.arn]
    }
  }

  statement {
    sid    = "AllowStorageClassAnalysisGetBucketLocation"
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
    actions = ["s3:GetBucketLocation"]
    resources = [
      aws_s3_bucket.backup.arn
    ]
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [aws_s3_bucket.backup.arn]
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

  # Restic keeps snapshots for at most ~92 days, so a pack either dies within
  # that window or lives on indefinitely (data that never changes). Packs only
  # referenced by daily snapshots are pruned ~30-32 days after upload; 35 days
  # moves what is left straight to Glacier IR, which is cheaper than Standard
  # or IA for both kinds even counting its 90-day minimum. Objects under
  # 128 KB (restic metadata) stay in Standard: IA and Glacier IR bill them as
  # 128 KB, and S3 does not transition them by default anyway.
  rule {
    id     = "backup-large-objects-to-gir"
    status = "Enabled"

    filter {
      object_size_greater_than = 131072 # 128 KB
    }

    transition {
      days          = 35
      storage_class = "GLACIER_IR"
    }
  }

  # Housekeeping — abort incomplete multipart uploads
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
