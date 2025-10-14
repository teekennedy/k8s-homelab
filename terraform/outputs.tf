output "backup_bucket_name" {
  description = "Name of the S3 backup bucket"
  value       = module.backup.bucket_name
  sensitive   = true
}

output "backup_bucket_region" {
  description = "AWS region of the S3 backup bucket"
  value       = module.backup.bucket_region
}

output "backup_iam_user_name" {
  description = "Name of the IAM user for backup operations"
  value       = module.backup.iam_user_name
}

output "backup_access_key_id" {
  description = "Access Key ID for the backup IAM user"
  value       = module.backup.access_key_id
}

output "backup_encrypted_secret_access_key" {
  description = "Encrypted Secret Access Key for the backup IAM user"
  value       = module.backup.encrypted_secret_access_key
  sensitive   = true
}

