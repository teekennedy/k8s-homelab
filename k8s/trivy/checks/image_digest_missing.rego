# METADATA
# title: "Image not pinned by digest"
# description: "A tag can be moved to a different image; a digest cannot."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0004
#   severity: HIGH
#   recommended_action: "Append the image's digest to 'containers[].image' (@sha256:...)."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0004

import rego.v1

import data.lib.homelab.workload

deny contains res if {
	some container in workload.containers
	not regex.match(`@sha256:[a-f0-9]{64}$`, object.get(container, "image", ""))
	res := result.new(sprintf("%s should pin 'image' by digest", [workload.describe(container)]), container)
}
