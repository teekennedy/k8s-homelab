locals {
  account_id                  = data.aws_caller_identity.this.account_id
  ses_domain_identity_arn     = "arn:${data.aws_partition.this.partition}:ses:${var.aws_region}:${local.account_id}:identity/${aws_ses_domain_identity.this.domain}"
  ses_notification_topic_name = "${replace(var.domain, ".", "-")}-ses-bounces-complaints"
  ses_dns_records = [
    {
      id      = "verification-txt"
      name    = "_amazonses"
      type    = "TXT"
      content = aws_ses_domain_identity.this.verification_token
    },
    {
      id      = "spf-txt"
      name    = "mail"
      type    = "TXT"
      content = "\"v=spf1 include:amazonses.com ~all\""
    },
    {
      id       = "mx"
      name     = "mail"
      type     = "MX"
      priority = 10
      content  = "feedback-smtp.${var.aws_region}.amazonses.com"
    },
    {
      id      = "dmarc"
      name    = "_dmarc"
      type    = "TXT"
      content = "\"v=DMARC1; p=none;\""
    }
  ]
}
