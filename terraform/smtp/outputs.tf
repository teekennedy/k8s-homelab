output "smtp_access_key_id" {
  value     = aws_iam_access_key.smtp.id
  sensitive = false
}

output "smtp_secret_access_key" {
  value     = data.external.smtp_secret_access_key.result.value
  sensitive = true
}
