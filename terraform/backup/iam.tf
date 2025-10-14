resource "aws_iam_user" "restic_backup" {
  name = "restic-backup-user"
  path = "/backup/"

  tags = {
    Name = "Restic Backup User"
  }
}

resource "aws_iam_access_key" "restic_backup" {
  user    = aws_iam_user.restic_backup.name
  pgp_key = var.pgp_key
}

data "aws_iam_policy_document" "restic_backup_policy" {
  statement {
    sid       = "ListOnlyWithinPrefix"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.backup.arn]

    # Let the user list only keys under var.restic_prefix
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["${var.restic_prefix}*", var.restic_prefix]
    }
  }

  statement {
    sid    = "ObjectCRUDWithinPrefix"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:ListMultipartUploadParts",
      "s3:AbortMultipartUpload"
    ]
    resources = ["${aws_s3_bucket.backup.arn}/${var.restic_prefix}*"]
  }
}

resource "aws_iam_user_policy" "restic_backup" {
  name   = "restic-backup-policy"
  user   = aws_iam_user.restic_backup.name
  policy = data.aws_iam_policy_document.restic_backup_policy.json
}

data "aws_iam_policy_document" "backup_kms_access" {
  statement {
    sid    = "AllowUseOfBackupKmsKey"
    effect = "Allow"
    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey"
    ]
    resources = [aws_kms_key.backup.arn]
  }
}

resource "aws_iam_policy" "backup_kms" {
  name   = "${var.backup_bucket_name}-kms-access"
  policy = data.aws_iam_policy_document.backup_kms_access.json
}

resource "aws_iam_user_policy_attachment" "restic_backup_kms" {
  user       = aws_iam_user.restic_backup.name
  policy_arn = aws_iam_policy.backup_kms.arn
}
