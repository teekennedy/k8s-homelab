# METADATA
# title: "Ingress without TLS"
# description: "An Ingress with no 'tls' section is served over plain HTTP, or with the controller's default certificate."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0008
#   severity: MEDIUM
#   recommended_action: "List the Ingress's hosts under 'spec.tls'."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0008

import rego.v1

deny contains res if {
	input.kind == "Ingress"
	count(object.get(input.spec, "tls", [])) == 0
	res := result.new(sprintf("Ingress '%s' should set 'spec.tls'", [input.metadata.name]), input.spec)
}
