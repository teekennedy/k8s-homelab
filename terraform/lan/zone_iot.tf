# Network and firewall rules for the IoT frewall zone
resource "unifi_firewall_zone" "iot" {
  name = "IoT"
  network_ids = [
    unifi_network.vlans["rain_machine"].id,
  ]
}

# Allow internal source subnets -> RainMachine on tcp/8080 (HTTP UI).
resource "unifi_firewall_policy" "allow_http_rain_machine" {
  name        = "Allow HTTP RainMachine"
  description = "Allow access to the RainMachine HTTP API"
  action      = "ALLOW"
  protocol    = "tcp"
  ip_version  = "BOTH"

  # auto-create the established/related return rule
  create_allow_respond = true

  source = {
    zone_id         = data.unifi_firewall_zone.internal.id
    matching_target = "NETWORK"
    network_ids = [
      unifi_network.vlans["home_assistant"].id,
      unifi_network.vlans["trusted"].id,
      data.unifi_network.default.id,
    ]
  }

  destination = {
    zone_id         = unifi_firewall_zone.iot.id
    matching_target = "NETWORK"
    network_ids = [
      unifi_network.vlans["rain_machine"].id,
    ]
    port               = "8080"
    port_matching_type = "SPECIFIC"
  }
}
