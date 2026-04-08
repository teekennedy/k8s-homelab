resource "unifi_wan" "primary" {
  name = "Primary (WAN1)"

  type    = "dhcp"
  type_v6 = "dhcpv6"
  dhcpv6 = {
    pd_size = 56
  }
  dns = {
    primary        = "1.0.0.1"
    secondary      = "1.1.1.1"
    ipv6_primary   = "2606:4700:4700::1001",
    ipv6_secondary = "2606:4700:4700::1111",
  }
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
#   multicast_dns  = true
#
#   dhcp_start = "10.69.0.11"
#   dhcp_stop  = "10.69.1.254"
# }

locals {
  managed_vlans = {
    for vlan_name, vlan in lookup(local.inventory, "vlans", {}) :
    vlan_name => {
      name   = coalesce(try(vlan.name, null), vlan_name)
      subnet = try(vlan.subnet, null) != null ? join("", [cidrhost(vlan.subnet, 1), regex("/\\d+$", vlan.subnet)]) : null
      vlan   = try(vlan.vlan_tag, null)
      dhcp_server = try(vlan.dhcp.enabled, false) ? {
        enabled         = vlan.dhcp.enabled
        gateway_enabled = try(vlan.dhcp.gateway_enabled, false)
        dns_enabled     = try(vlan.dhcp.dns_enabled, true)
        # NB: I had to explicitly set this or tofu would error out saying the provider produced an inconsistent result.
        boot = {
          enabled = false
        }
        start = coalesce(
          try(vlan.dhcp.start, null),
          try(vlan.subnet, null) != null ? cidrhost(vlan.subnet, 8) : null,
        )
        stop = coalesce(
          try(vlan.dhcp.stop, null),
          try(vlan.subnet, null) != null ? cidrhost(vlan.subnet, -2) : null,
        )
      } : null
      multicast_dns     = try(vlan.multicast_dns, false)
      network_isolation = try(vlan.network_isolation, false)
      auto_scale        = try(vlan.auto_scale, false)
      lte_lan           = try(vlan.lte_lan, false)
    }
    if vlan_name != "default"
  }
}

resource "unifi_network" "vlans" {
  for_each = local.managed_vlans

  name = each.value.name

  multicast_dns = each.value.multicast_dns

  subnet = each.value.subnet
  vlan   = each.value.vlan

  dhcp_server        = each.value.dhcp_server
  setting_preference = "manual"
  network_isolation  = each.value.network_isolation
  auto_scale         = each.value.auto_scale
  lte_lan            = each.value.lte_lan
}
