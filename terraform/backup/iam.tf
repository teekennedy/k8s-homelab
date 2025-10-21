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

#tfsec:ignore:AVD-AWS-0143
resource "aws_iam_user" "backup" {
  for_each = var.backup_users

  name = each.key
  path = lookup(each.value, "path", "/backup/")
}

resource "aws_iam_access_key" "backup" {
  for_each = aws_iam_user.backup

  user    = each.value.name
  pgp_key = var.pgp_key
}

data "aws_iam_policy_document" "backup_policy" {
  for_each = var.backup_users

  statement {
    sid       = "ListOnlyWithinPrefix"
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.backup.arn]

    # Limit listing to the allowed prefix for this user
    condition {
      test     = "StringLike"
      variable = "s3:prefix"
      values   = ["${each.value.s3_prefix}*", each.value.s3_prefix]
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
    resources = ["${aws_s3_bucket.backup.arn}/${each.value.s3_prefix}*"]
  }
}

resource "aws_iam_user_policy" "backup" {
  for_each = aws_iam_user.backup

  name   = "${each.key}-backup-policy"
  user   = each.value.name
  policy = data.aws_iam_policy_document.backup_policy[each.key].json
}

resource "aws_iam_user_policy_attachment" "backup_kms" {
  for_each = aws_iam_user.backup

  user       = each.value.name
  policy_arn = aws_iam_policy.backup_kms.arn
}
