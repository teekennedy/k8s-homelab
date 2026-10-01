# Network and firewall rules for the UPS network management card
resource "unifi_firewall_zone" "ups" {
  name = "UPS"
  network_ids = [
    unifi_network.vlans["ups"].id,
  ]
}

# SNMP clients address the card by IP so they don't depend on DNS during an outage
resource "unifi_client" "ups" {
  mac        = "00:0c:15:06:28:ab"
  name       = "CyberPower UPS"
  note       = "RMCARD205 network management card for the CP1500PFCRM2U UPS"
  network_id = unifi_network.vlans["ups"].id
  fixed_ip   = "10.69.110.15"

  allow_existing         = true
  skip_forget_on_destroy = true
}

# Web UI from anywhere on the LAN
resource "unifi_firewall_policy" "allow_ups_web_internal" {
  name        = "Allow UPS Web (Internal)"
  description = "Allow internal networks to reach the UPS web UI"
  action      = "ALLOW"
  protocol    = "tcp"
  ip_version  = "IPV4"

  create_allow_respond = true

  source = {
    zone_id         = data.unifi_firewall_zone.internal.id
    matching_target = "ANY"
  }

  destination = {
    zone_id            = unifi_firewall_zone.ups.id
    matching_target    = "ANY"
    port               = "443"
    port_matching_type = "SPECIFIC"
  }
}

# Home Assistant's SNMP sensors
resource "unifi_firewall_policy" "allow_ups_snmp_home_assistant" {
  name        = "Allow UPS SNMP (Home Assistant)"
  description = "Allow Home Assistant to poll the UPS over SNMP"
  action      = "ALLOW"
  protocol    = "udp"
  ip_version  = "IPV4"

  create_allow_respond = true

  source = {
    zone_id         = data.unifi_firewall_zone.internal.id
    matching_target = "NETWORK"
    network_ids = [
      # TODO remove default after Home Assistant is migrated
      data.unifi_network.default.id,
      unifi_network.vlans["home_assistant"].id,
    ]
  }

  destination = {
    zone_id            = unifi_firewall_zone.ups.id
    matching_target    = "ANY"
    port               = "161"
    port_matching_type = "SPECIFIC"
  }
}

# k8s nodes poll the UPS to decide when to shut down
resource "unifi_firewall_policy" "allow_ups_snmp_k8s" {
  name        = "Allow UPS SNMP (k8s)"
  description = "Allow k8s nodes to poll the UPS over SNMP"
  action      = "ALLOW"
  protocol    = "udp"
  ip_version  = "IPV4"

  create_allow_respond = true

  source = {
    zone_id         = data.unifi_firewall_zone.dmz.id
    matching_target = "NETWORK"
    network_ids     = [unifi_network.vlans["k8s"].id]
  }

  destination = {
    zone_id            = unifi_firewall_zone.ups.id
    matching_target    = "ANY"
    port               = "161"
    port_matching_type = "SPECIFIC"
  }
}

resource "unifi_firewall_policy" "allow_ups_web_k8s" {
  name        = "Allow UPS Web (k8s)"
  description = "Allow k8s nodes to reach the UPS web UI"
  action      = "ALLOW"
  protocol    = "tcp"
  ip_version  = "IPV4"

  create_allow_respond = true

  source = {
    zone_id         = data.unifi_firewall_zone.dmz.id
    matching_target = "NETWORK"
    network_ids     = [unifi_network.vlans["k8s"].id]
  }

  destination = {
    zone_id            = unifi_firewall_zone.ups.id
    matching_target    = "ANY"
    port               = "443"
    port_matching_type = "SPECIFIC"
  }
}

# Custom zones allow all traffic to External by default, so outbound is limited
# to DNS, NTP, and HTTPS (firmware updates) with an allow followed by a block.
resource "unifi_firewall_policy" "allow_ups_external" {
  name        = "Allow UPS External"
  description = "Allow UPS DNS, NTP, and HTTPS"
  action      = "ALLOW"
  protocol    = "tcp_udp"
  ip_version  = "BOTH"

  source = {
    zone_id         = unifi_firewall_zone.ups.id
    matching_target = "ANY"
  }

  destination = {
    zone_id            = data.unifi_firewall_zone.external.id
    matching_target    = "ANY"
    port               = "53,123,443"
    port_matching_type = "SPECIFIC"
  }
}

# Policies are evaluated in creation order and the API can't reorder them, so
# this is created after, and replaced along with, the allow above.
resource "unifi_firewall_policy" "block_ups_external" {
  name        = "Block UPS External"
  description = "Block all other UPS traffic to the internet"
  action      = "BLOCK"
  protocol    = "all"
  ip_version  = "BOTH"

  source = {
    zone_id         = unifi_firewall_zone.ups.id
    matching_target = "ANY"
  }

  destination = {
    zone_id         = data.unifi_firewall_zone.external.id
    matching_target = "ANY"
  }

  depends_on = [unifi_firewall_policy.allow_ups_external]

  lifecycle {
    replace_triggered_by = [unifi_firewall_policy.allow_ups_external.id]
  }
}
