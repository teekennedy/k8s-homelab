resource "unifi_network" "wan" {
  name    = "Primary (WAN1)"
  purpose = "wan"

  wan_networkgroup = "WAN"
  wan_type         = "dhcp"
  wan_type_v6      = "dhcpv6"
  wan_prefixlen    = 56
  wan_dns = [
    "1.0.0.1",
    "1.1.1.1",
    # TODO add support for ipv6 DNS
    # "2606:4700:4700::1001",
    # "2606:4700:4700::1111",
  ]
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

resource "unifi_network" "okta_vlan" {
  name    = "okta"
  purpose = "corporate"
  # site    = local.unifi_site
  multicast_dns = false

  subnet                       = "10.69.70.0/26" # 62 usable IPs
  vlan_id                      = 700
  dhcp_start                   = "10.69.70.6"
  dhcp_stop                    = "10.69.70.62"
  dhcp_enabled                 = true
  ipv6_ra_enable               = false
  intra_network_access_enabled = false
}

