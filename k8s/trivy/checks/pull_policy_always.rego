# METADATA
# title: "Image pull policy is Always"
# description: "Images are pinned by digest, so pulling one again on every container start is wasted work and makes a registry outage a restart outage."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0003
#   severity: LOW
#   recommended_action: "Set 'containers[].imagePullPolicy' to IfNotPresent, or leave it unset."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0003

import rego.v1

import data.lib.homelab.workload

deny contains res if {
	some container in workload.containers
	container.imagePullPolicy == "Always"
	res := result.new(sprintf("%s should not set 'imagePullPolicy' to Always", [workload.describe(container)]), container)
}
