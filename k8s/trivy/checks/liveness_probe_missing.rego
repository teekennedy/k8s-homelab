# METADATA
# title: "Liveness probe missing"
# description: "Without a liveness probe the kubelet cannot restart a container that has hung."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0001
#   severity: MEDIUM
#   recommended_action: "Set 'containers[].livenessProbe'."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0001

import rego.v1

import data.lib.homelab.workload

deny contains res if {
	not workload.runs_to_completion
	some container in workload.main_containers
	not container.livenessProbe
	res := result.new(sprintf("%s should set 'livenessProbe'", [workload.describe(container)]), container)
}
