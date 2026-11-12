# METADATA
# title: "Sensitive environment variable set to a literal"
# description: "A credential written into a manifest is readable by anyone who can read the repository or the pod spec."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0006
#   severity: HIGH
#   recommended_action: "Load the value with 'valueFrom.secretKeyRef', or mount the Secret as a file."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0006

import rego.v1

import data.lib.homelab.workload

sensitive_name(name) if {
	regex.match(`(?i)(passw(or)?d|secret|token|credential|(api|access|private|encryption|signing)_?key)`, name)

	# By convention these name the file or the Secret that holds the
	# credential, or the public half of a key pair (AWS_ACCESS_KEY_ID).
	not regex.match(`(?i)_(file|path|dir|name|id)$`, name)
}

# A value built only from $(VAR) references carries no credential of its own.
literal(value) if {
	value != ""
	not contains(value, "$(")
}

deny contains res if {
	some container in workload.containers
	some env in container.env
	sensitive_name(env.name)
	literal(object.get(env, "value", ""))
	res := result.new(
		sprintf("%s should not set environment variable '%s' to a literal value", [workload.describe(container), env.name]),
		env,
	)
}
