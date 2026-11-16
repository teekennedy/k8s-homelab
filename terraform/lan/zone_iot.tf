# Network and firewall rules for the IoT frewall zone
resource "unifi_firewall_zone" "iot" {
  name = "IoT"
  network_ids = [
    unifi_network.vlans["rain_machine"].id,
    unifi_network.vlans["xcel"].id,
    unifi_network.vlans["esphome"].id,
    unifi_network.vlans["roombas"].id,
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

# Allow Voice Assistant to fetch TTS sound files from home assistant
resource "unifi_firewall_policy" "allow_voice_assistant_tts" {
  name        = "Allow Voice Assistant TTS"
  description = "Allow Voice Assistant to fetch TTS sound files from home assistant"
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

    port               = "443"
    port_matching_type = "SPECIFIC"
  }
}

# Allow the Voice PE to upload wake word captures to the wake-word-trainer
# collector (k8s/apps/wake-word-trainer). Matches the /27 that covers both
# MetalLB pools (internal 10.69.80.100-119, public 10.69.80.120-126) and none of
# the node addresses, so the rule survives a VIP reassignment without opening
# the nodes themselves.
resource "unifi_firewall_policy" "allow_esphome_wake_word_capture" {
  name        = "Allow ESPHome wake word capture"
  description = "Allow ESPHome devices to upload wake word clips to the cluster ingress"
  action      = "ALLOW"
  protocol    = "tcp"
  ip_version  = "IPV4"

  # DMZ -> IoT is blocked by default, so without this the SYN-ACK from the
  # ingress is dropped and the device's connect times out.
  create_allow_respond = true

  source = {
    zone_id         = unifi_firewall_zone.iot.id
    matching_target = "NETWORK"
    network_ids = [
      unifi_network.vlans["esphome"].id,
    ]
  }

  destination = {
    zone_id         = data.unifi_firewall_zone.dmz.id
    matching_target = "IP"
    ips             = ["10.69.80.96/27"]

    port               = "443"
    port_matching_type = "SPECIFIC"
  }
}
