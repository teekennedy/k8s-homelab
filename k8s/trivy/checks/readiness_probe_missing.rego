# METADATA
# title: "Readiness probe missing"
# description: "Without a readiness probe a container receives traffic before it can serve it."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0002
#   severity: MEDIUM
#   recommended_action: "Set 'containers[].readinessProbe'."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0002

import rego.v1

import data.lib.homelab.workload

deny contains res if {
	not workload.runs_to_completion
	some container in workload.main_containers
	not container.readinessProbe
	res := result.new(sprintf("%s should set 'readinessProbe'", [workload.describe(container)]), container)
}
