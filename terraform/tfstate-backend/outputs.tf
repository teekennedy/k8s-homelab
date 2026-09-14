output "bucket_name" {
  description = "Name of the S3 bucket holding OpenTofu state."
  value       = aws_s3_bucket.state.id
}

output "bucket_arn" {
  description = "ARN of the S3 bucket holding OpenTofu state."
  value       = aws_s3_bucket.state.arn
}

output "dynamodb_table_name" {
  description = "Name of the DynamoDB table used for state locking."
  value       = aws_dynamodb_table.locks.name
}

output "region" {
  description = "AWS region the state bucket and lock table live in."
  value       = data.aws_region.current.region
}

output "account_id" {
  description = "AWS account that owns the state backend."
  value       = data.aws_caller_identity.current.account_id
}
