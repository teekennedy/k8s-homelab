# METADATA
# title: "Longhorn volume outside a recurring-job group"
# description: "Longhorn puts an unlabelled volume in its 'default' group, which no recurring job targets, so the volume is silently never backed up or trimmed."
# scope: package
# schemas:
#   - input: schema["kubernetes"]
# custom:
#   id: HL-0005
#   severity: HIGH
#   recommended_action: "Label the volume recurring-job.longhorn.io/source=enabled and with recurring-job-group.longhorn.io/backup or recurring-job-group.longhorn.io/ephemeral."
#   input:
#     selector:
#       - type: kubernetes
package user.kubernetes.HL0005

import rego.v1

groups := {"recurring-job-group.longhorn.io/backup", "recurring-job-group.longhorn.io/ephemeral"}

# An unset class means the cluster default, which is Longhorn. An empty string
# binds a static PV and is not checked.
on_longhorn(spec, field) if not field in object.keys(spec)

on_longhorn(spec, field) if startswith(spec[field], "longhorn")

grouped(metadata) if {
	metadata.labels["recurring-job.longhorn.io/source"] == "enabled"
	some group in groups
	group in object.keys(metadata.labels)
}

# ungrouped_claim holds for a PersistentVolumeClaim, or a template of one, that
# Longhorn provisions without the labels.
ungrouped_claim(claim) if {
	on_longhorn(claim.spec, "storageClassName")
	not grouped(object.get(claim, "metadata", {}))
}

message(what) := sprintf(
	"%s of %s '%s' is a Longhorn volume outside the backup and ephemeral recurring-job groups",
	[what, input.kind, input.metadata.name],
)

deny contains res if {
	input.kind == "PersistentVolumeClaim"
	ungrouped_claim(input)
	res := result.new(message("The volume"), input.metadata)
}

deny contains res if {
	input.kind == "StatefulSet"
	some claim in input.spec.volumeClaimTemplates
	ungrouped_claim(claim)
	res := result.new(message(sprintf("volumeClaimTemplate '%s'", [claim.metadata.name])), claim)
}

# The Prometheus operator's kinds carry one claim template each.
deny contains res if {
	startswith(input.apiVersion, "monitoring.coreos.com/")
	input.kind in {"Alertmanager", "Prometheus"}
	claim := input.spec.storage.volumeClaimTemplate
	ungrouped_claim(claim)
	res := result.new(message("The volumeClaimTemplate"), claim)
}

# CloudNativePG copies spec.inheritedMetadata onto the claims it creates.
deny contains res if {
	startswith(input.apiVersion, "postgresql.cnpg.io/")
	input.kind == "Cluster"
	on_longhorn(input.spec.storage, "storageClass")
	not grouped(object.get(input.spec, "inheritedMetadata", {}))
	res := result.new(message("The storage"), input.spec)
}
