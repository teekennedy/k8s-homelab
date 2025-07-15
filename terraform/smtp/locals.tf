locals {
  account_id = data.aws_caller_identity.this.account_id
  region     = data.aws_region.this.name
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
      content  = "feedback-smtp.${local.region}.amazonses.com"
    },
    {
      id      = "dmarc"
      name    = "_dmarc"
      type    = "TXT"
      content = "\"v=DMARC1; p=none;\""
    }
  ]
}
