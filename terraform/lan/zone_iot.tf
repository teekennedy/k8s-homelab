# Network and firewall rules for the IoT frewall zone
resource "unifi_firewall_zone" "iot" {
  name = "IoT"
  network_ids = [
    unifi_network.vlans["xcel"].id,
    unifi_network.vlans["esphome"].id,
    unifi_network.vlans["roombas"].id,
    unifi_network.vlans["rain_machine"].id,
  ]
}

# Allow specific internal subnets -> IoT zone
resource "unifi_firewall_policy" "allow_iot" {
  name        = "Allow IoT Traffic"
  description = "Allow traffic to IoT zone"
  action      = "ALLOW"
  protocol    = "all"
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
    matching_target = "ANY"
  }
}

# Allow ESPHome devices to communicate with home assistant via native API
# https://esphome.io/components/api/
resource "unifi_firewall_policy" "allow_esphome_api" {
  name        = "Allow ESPHome API"
  description = "Allow ESPHome devices to communicate with home assistant via native API"
  action      = "ALLOW"
  protocol    = "tcp"
  ip_version  = "BOTH"

  source = {
    zone_id         = unifi_firewall_zone.iot.id
    matching_target = "NETWORK"
    network_ids = [
      unifi_network.vlans["esphome"].id,
    ]
  }

  destination = {
    zone_id         = data.unifi_firewall_zone.internal.id
    matching_target = "NETWORK"
    network_ids = [
      data.unifi_network.default.id,
      # TODO remove default after Home Assistant is migrated
      unifi_network.vlans["home_assistant"].id,
    ]

    port               = "6053"
    port_matching_type = "SPECIFIC"
  }
}
