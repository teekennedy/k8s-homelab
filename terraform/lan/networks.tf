resource "unifi_network" "wan" {
  name    = "Primary (WAN1)"
  purpose = "wan"

  wan_networkgroup    = "WAN"
  wan_type            = "dhcp"
  wan_type_v6         = "dhcpv6"
  wan_prefixlen       = 56
  wan_dhcp_v6_pd_size = 56
  wan_dns = [
    "1.0.0.1",
    "1.1.1.1",
  ]
  # TODO this setting does not yet exist in the provider - has been set manually
  # wan_ipv6_dns = [
  #   "2606:4700:4700::1001",
  #   "2606:4700:4700::1111",
  # ]
}

# TODO I've matched this to the existing config as closely as possible,
#      but every time I run an apply, the network becomes unreachable.
# resource "unifi_network" "default" {
#   name    = "Default"
#   purpose = "corporate"
#
#   subnet         = "10.69.0.9/23" # ?? usable hosts
#   domain_name    = "lan"
#   vlan_id        = 1 # NB: the API shows vlan id 0 for this network.
#   dhcp_enabled   = false
#   ipv6_ra_enable = false
#   multicast_dns  = true
#
#   dhcp_start = "10.69.0.11"
#   dhcp_stop  = "10.69.1.254"
# }

locals {
  managed_vlans = {
    for vlan_name, vlan in lookup(local.inventory, "vlans", {}) :
    vlan_name => {
      name         = coalesce(try(vlan.name, null), vlan_name)
      purpose      = coalesce(try(vlan.purpose, null), "corporate")
      subnet       = try(vlan.subnet, null)
      vlan_id      = try(vlan.vlan_tag, null)
      dhcp_enabled = try(vlan.dhcp.enabled, false)
      dhcp_start = try(vlan.dhcp.enabled, false) ? coalesce(
        try(vlan.dhcp.start, null),
        try(vlan.subnet, null) != null ? cidrhost(vlan.subnet, 8) : null,
      ) : null
      dhcp_stop = try(vlan.dhcp.enabled, false) ? coalesce(
        try(vlan.dhcp.stop, null),
        try(vlan.subnet, null) != null ? cidrhost(vlan.subnet, -2) : null,
      ) : null
      multicast_dns                = try(vlan.multicast_dns, false)
      ipv6_ra_enable               = try(vlan.ipv6_ra_enable, false)
      intra_network_access_enabled = try(vlan.intra_network_access_enabled, false)
    }
    if vlan_name != "default"
  }
}

resource "unifi_network" "vlans" {
  for_each = local.managed_vlans

  name    = each.value.name
  purpose = each.value.purpose

  multicast_dns = each.value.multicast_dns

  subnet  = each.value.subnet
  vlan_id = each.value.vlan_id

  dhcp_start   = each.value.dhcp_start
  dhcp_stop    = each.value.dhcp_stop
  dhcp_enabled = each.value.dhcp_enabled

  ipv6_ra_enable               = each.value.ipv6_ra_enable
  intra_network_access_enabled = each.value.intra_network_access_enabled
}
