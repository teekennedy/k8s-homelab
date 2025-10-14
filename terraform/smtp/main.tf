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
    sid       = "AllowSendMail"
    actions   = ["ses:SendRawEmail"]
    resources = ["arn:${data.aws_partition.this.partition}:ses:${local.region}:${local.account_id}:identity/*"]

    condition {
      test     = "StringLike"
      variable = "ses:FromAddress"
      values = [
        "*@${var.domain}"
      ]
    }
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
  user    = aws_iam_user.mail.name
  pgp_key = var.pgp_key
}

resource "cloudflare_dns_record" "ses" {
  for_each = { for rec in local.ses_dns_records : rec.id => rec }
  zone_id  = data.cloudflare_zones.zone.result[0].id
  name     = "${each.value.name}.${var.domain}"
  type     = each.value.type
  content  = each.value.content
  priority = try(each.value.priority, null) # MX records require a priority field
  proxied  = false
  ttl      = 1 # Auto
}

resource "cloudflare_dns_record" "ses_dkim" {
  # The documentation for this resource shows hardcoding count to 3
  count   = 3
  zone_id = data.cloudflare_zones.zone.result[0].id
  name    = "${aws_ses_domain_dkim.this.dkim_tokens[count.index]}._domainkey.${var.domain}"
  type    = "CNAME"
  content = "${aws_ses_domain_dkim.this.dkim_tokens[count.index]}.dkim.amazonses.com"
  proxied = false
  ttl     = 1 # Auto
}
data "external" "smtp_secret_access_key" {
  query = {
    enc_secret = aws_iam_access_key.smtp.encrypted_ses_smtp_password_v4
  }

  program = [
    "sh",
    "-c",
    (<<-EOF
      echo '${aws_iam_access_key.smtp.encrypted_ses_smtp_password_v4}' \
        | base64 -d \
	      | gpg --decrypt \
	      | jq -R '{value: .}'
      EOF
    ),
  ]
}

output "smtp_access_key_id" {
  value     = aws_iam_access_key.smtp.id
  sensitive = false
}

output "smtp_secret_access_key" {
  value     = data.external.smtp_secret_access_key.result.value
  sensitive = true
}

