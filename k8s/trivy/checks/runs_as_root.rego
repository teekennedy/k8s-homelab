# METADATA
# title: "Container can run as root"
# description: "A container that neither requires a non-root user nor names one runs as whatever its image defaults to, which is usually root."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0009
#   severity: MEDIUM
#   recommended_action: "Set 'securityContext.runAsNonRoot' to true or 'securityContext.runAsUser' above 0, on the pod or the container."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0009

import rego.v1

import data.lib.homelab.workload

pod_context := object.get(workload.pod.spec, "securityContext", {})

# A container's own securityContext overrides the pod's field by field.
effective(context, field) := object.get(context, field, object.get(pod_context, field, null))

# The user a container runs as decides, wherever it is set; runAsNonRoot only
# matters when neither the container nor the pod names one.
non_root(context) if {
	user := effective(context, "runAsUser")
	is_number(user)
	user > 0
}

non_root(context) if {
	effective(context, "runAsUser") == null
	effective(context, "runAsNonRoot") == true
}

deny contains res if {
	some container in workload.containers
	not non_root(object.get(container, "securityContext", {}))
	res := result.new(
		sprintf("%s should set 'securityContext.runAsNonRoot' to true or 'securityContext.runAsUser' above 0", [workload.describe(container)]),
		container,
	)
}
