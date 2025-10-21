output "backup_bucket_name" {
  description = "Name of the S3 backup bucket"
  value       = module.backup.bucket_name
  sensitive   = true
}

output "backup_bucket_region" {
  description = "AWS region of the S3 backup bucket"
  value       = module.backup.bucket_region
}

output "backup_iam_users" {
  description = "IAM users for backup operations keyed by configuration key."
  value       = module.backup.iam_users
}

output "backup_access_keys" {
  description = "Access key material for backup IAM users keyed by configuration key."
  value       = module.backup.access_keys
  sensitive   = true
}
