locals {
  dns_edit_permission_groups = [
    for group in
    data.cloudflare_api_token_permission_groups_list.all.result :
    { id = group.id }
    if group.name == "Zone Read" || group.name == "DNS Write"
  ]
}
