# METADATA
# title: "PodDisruptionBudget allows no disruptions"
# description: "A budget that permits zero evictions blocks every node drain, and with it every unattended reboot."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0007
#   severity: MEDIUM
#   recommended_action: "Allow at least one disruption: 'maxUnavailable' above zero, or 'minAvailable' below 100%."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0007

import rego.v1

blocked(spec) := "'maxUnavailable' is zero" if spec.maxUnavailable in {0, "0", "0%"}

blocked(spec) := "'minAvailable' is 100%" if spec.minAvailable == "100%"

deny contains res if {
	input.kind == "PodDisruptionBudget"
	reason := blocked(input.spec)
	res := result.new(
		sprintf("PodDisruptionBudget '%s' allows no disruptions: %s", [input.metadata.name, reason]),
		input.spec,
	)
}
