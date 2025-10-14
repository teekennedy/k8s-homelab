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

output "iam_user_name" {
  description = "Name of the IAM user for backup operations"
  value       = aws_iam_user.restic_backup.name
}

output "iam_user_arn" {
  description = "ARN of the IAM user for backup operations"
  value       = aws_iam_user.restic_backup.arn
}

output "access_key_id" {
  description = "Access Key ID for the backup IAM user"
  value       = aws_iam_access_key.restic_backup.id
}

output "encrypted_secret_access_key" {
  description = "PGP encrypted Secret Access Key for the backup IAM user"
  value       = aws_iam_access_key.restic_backup.encrypted_secret
}

output "kms_key_arn" {
  description = "KMS key ARN used for SSE-KMS on the backup bucket"
  value       = aws_kms_key.backup.arn
}

output "kms_key_alias" {
  description = "KMS key alias"
  value       = aws_kms_alias.backup.name
}
