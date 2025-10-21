data "aws_iam_policy_document" "kms_key" {
  # Full admin on the key to the account root
  statement {
    sid    = "AllowAccountRootAdmin"
    effect = "Allow"
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
    actions = [
      "kms:*"
    ]
    resources = ["*"]
  }

  # Allow the backup IAM users to use the key *via S3 only* and only
  # when the S3 encryption context references THIS bucket/prefix.
  statement {
    sid    = "AllowBackupUserUseKeyViaS3ForThisBucket"
    effect = "Allow"
    principals {
      type        = "AWS"
      identifiers = [for _, user in aws_iam_user.backup : user.arn]
    }
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey"
    ]
    resources = ["*"]

    # Constrain usage so the key can only be used by S3 in this region,
    # and only for objects whose S3 ARN matches your bucket/prefix.
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["s3.${data.aws_region.current.region}.amazonaws.com"]
    }

    # Matches either the bucket or any object in the bucket.
    # The S3 encryption context will include these values at encrypt/decrypt time.
    condition {
      test     = "ForAnyValue:StringLike"
      variable = "kms:EncryptionContext:aws:s3:arn"
      values = [
        aws_s3_bucket.backup.arn,
        "${aws_s3_bucket.backup.arn}/*"
      ]
    }
  }
}

resource "aws_kms_key" "backup" {
  description             = "KMS key for ${local.bucket_name} S3 bucket"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.kms_key.json

  tags = {
    Name        = "${var.environment}-backups-kms"
    Purpose     = "Encrypts S3 objects for backup"
    Environment = var.environment
  }
}

resource "aws_kms_alias" "backup" {
  name          = "alias/${var.environment}-backups"
  target_key_id = aws_kms_key.backup.key_id
}
