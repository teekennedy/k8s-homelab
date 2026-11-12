# METADATA
# title: "Resource requests or memory limit missing"
# description: "The scheduler places a pod by its requests, and a container with no memory limit can exhaust its node."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0010
#   severity: MEDIUM
#   recommended_action: "Set 'resources.requests.cpu', 'resources.requests.memory' and 'resources.limits.memory' on every container."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0010

import rego.v1

import data.lib.homelab.workload

# CPU limits are left out on purpose: they throttle, and requests are what
# scheduling needs.
required := [["requests", "cpu"], ["requests", "memory"], ["limits", "memory"]]

# Init containers that run to completion are not checked.
deny contains res if {
	some container in workload.main_containers
	some field in required
	object.get(container, array.concat(["resources"], field), null) == null
	res := result.new(
		sprintf("%s should set 'resources.%s'", [workload.describe(container), concat(".", field)]),
		container,
	)
}
