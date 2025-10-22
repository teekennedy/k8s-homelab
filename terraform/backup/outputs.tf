output "bucket_name" {
  description = "Name of the S3 backup bucket"
  value       = local.bucket_name
}

output "bucket_arn" {
  description = "ARN of the S3 backup bucket"
  value       = aws_s3_bucket.backup.arn
}

output "bucket_region" {
  description = "AWS region of the S3 backup bucket"
  value       = data.aws_region.current.region
}

output "iam_users" {
  description = "IAM users for backup operations keyed by configuration key."
  value = {
    for key, user in aws_iam_user.backup :
    key => {
      name = user.name
      arn  = user.arn
    }
  }
}

output "access_keys" {
  description = "Access keys for backup IAM users keyed by configuration key."
  value = {
    for key, access_key in aws_iam_access_key.backup :
    key => {
      AWS_ACCESS_KEY_ID     = access_key.id
      AWS_SECRET_ACCESS_KEY = access_key.secret
    }
  }
}

output "kms_key_arn" {
  description = "KMS key ARN used for SSE-KMS on the backup bucket"
  value       = aws_kms_key.backup.arn
}

output "kms_key_alias" {
  description = "KMS key alias"
  value       = aws_kms_alias.backup.name
}
