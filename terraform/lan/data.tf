data "sops_file" "tfvars" {
  source_file = "tfvars.sops.yaml"
}

# The default network
data "unifi_network" "default" {
  name = "Default"
}

# The auto-created zones
data "unifi_firewall_zone" "internal" {
  name = "Internal"
}

data "unifi_firewall_zone" "dmz" {
  name = "Dmz"
}

data "unifi_firewall_zone" "vpn" {
  name = "Vpn"
}
