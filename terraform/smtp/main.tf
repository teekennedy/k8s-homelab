resource "aws_iam_user" "mail" {
  name = var.username
}

resource "aws_iam_user_policy_attachment" "send_mail" {
  policy_arn = aws_iam_policy.send_mail.arn
  user       = aws_iam_user.mail.name
}

resource "aws_iam_policy" "send_mail" {
  name   = "${var.username}-send-mail"
  policy = data.aws_iam_policy_document.send_mail.json
}

data "aws_iam_policy_document" "send_mail" {
  statement {
    actions   = ["ses:SendRawEmail"]
    resources = [local.ses_identity_arn]
  }
}

resource "aws_ses_domain_identity" "this" {
  domain = var.domain
}

resource "aws_ses_domain_dkim" "this" {
  domain = aws_ses_domain_identity.this.domain
}

resource "aws_ses_domain_mail_from" "this" {
  domain           = aws_ses_domain_identity.this.domain
  mail_from_domain = "mail.${var.domain}"
}

resource "aws_ses_domain_identity_verification" "this" {
  domain = aws_ses_domain_identity.this.domain
}

resource "aws_iam_access_key" "smtp" {
  user = aws_iam_user.mail.name
}

output "ses_mx_records" {
  value = [
    {
      name     = aws_ses_domain_mail_from.this.mail_from_domain
      type     = "MX"
      priority = 10
      value    = "feedback-smtp.${local.region}.amazonses.com"
    }
  ]
  sensitive = false
}

output "ses_txt_records" {
  value = flatten([
    [
      {
        name  = aws_ses_domain_identity.this.domain
        type  = "TXT"
        value = aws_ses_domain_identity.this.verification_token
      }
    ],
    [
      for k, v in aws_ses_domain_dkim.this.dkim_tokens : {
        name  = "${v}._domainkey.${var.domain}"
        type  = "CNAME"
        value = "${v}.dkim.amazonses.com"
      }
    ],
    [
      {
        name  = aws_ses_domain_mail_from.this.mail_from_domain
        type  = "TXT"
        value = "v=spf1 include:amazonses.com ~all"
      }
    ]
  ])
  sensitive = false
}

output "smtp_access_key_id" {
  value     = aws_iam_access_key.smtp.id
  sensitive = false
}

output "smtp_secret_access_key" {
  value     = aws_iam_access_key.smtp.secret
  sensitive = true
}

