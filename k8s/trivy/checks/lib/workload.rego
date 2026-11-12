# METADATA
# description: Pod-spec helpers shared by the checks in this directory.
# custom:
#   library: true
package lib.homelab.workload

import rego.v1

kind := input.kind

name := input.metadata.name

# Jobs and CronJobs run to completion, so the checks that only make sense for
# a long-running pod skip them.
runs_to_completion if kind in {"Job", "CronJob"}

pod := input if kind == "Pod"

pod := input.spec.template if kind in {
	"DaemonSet", "Deployment", "Job", "ReplicaSet", "ReplicationController", "StatefulSet",
}

pod := input.spec.jobTemplate.spec.template if kind == "CronJob"

# main_containers are the containers that run for the life of the pod: the
# regular ones, and init containers declared as sidecars.
main_containers contains container if some container in pod.spec.containers

main_containers contains container if {
	some container in pod.spec.initContainers
	container.restartPolicy == "Always"
}

containers contains container if some container in main_containers

containers contains container if some container in pod.spec.initContainers

# describe names a container in a failure message.
describe(container) := sprintf("Container '%s' of %s '%s'", [container.name, kind, name])
