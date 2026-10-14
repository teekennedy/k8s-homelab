"""Black-box tests for provisioning from the aliased storage classes."""

import time

import pytest
from kubernetes import client

# One class per storage release the aliases stand in for.
STORAGE_CLASSES = ["longhorn", "longhorn-strict-local", "nfs", "smb"]

# Provisioning runs a helper pod, so this has to cover an image pull on a
# cluster that has never provisioned a volume before.
PROVISION_TIMEOUT = 120.0
POLL_INTERVAL = 1.0


def consumer(name: str, claim: str) -> client.V1Pod:
    """A pod that mounts claim.

    The classes bind on first consumer, so a claim stays Pending until a pod
    that uses it is scheduled.
    """
    return client.V1Pod(
        metadata=client.V1ObjectMeta(name=name),
        spec=client.V1PodSpec(
            restart_policy="Never",
            containers=[
                client.V1Container(
                    name="consumer",
                    image="registry.k8s.io/pause:3.10",
                    volume_mounts=[
                        client.V1VolumeMount(name="data", mount_path="/data")
                    ],
                )
            ],
            volumes=[
                client.V1Volume(
                    name="data",
                    persistent_volume_claim=client.V1PersistentVolumeClaimVolumeSource(
                        claim_name=claim
                    ),
                )
            ],
        ),
    )


def await_bound(
    core_v1: client.CoreV1Api, namespace: str, claim: str, deadline: float
) -> None:
    """Poll until the claim is Bound, or fail with the phase it was left in.

    This is the assertion, not a prelude to one.
    """
    while True:
        phase = core_v1.read_namespaced_persistent_volume_claim(
            name=claim, namespace=namespace
        ).status.phase
        if phase == "Bound":
            return
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"timed out after {PROVISION_TIMEOUT:.0f}s waiting for {namespace}/"
                f"{claim} to be Bound; it is {phase}"
            )
        time.sleep(POLL_INTERVAL)


@pytest.fixture(scope="module")
def deadline(core_v1: client.CoreV1Api, namespace: str) -> float:
    """Claim a volume from every class, and return when they are all due.

    Claimed together so the classes provision side by side and the waits
    overlap, instead of each test paying for its own.
    """
    for storage_class in STORAGE_CLASSES:
        core_v1.create_namespaced_persistent_volume_claim(
            namespace=namespace,
            body=client.V1PersistentVolumeClaim(
                metadata=client.V1ObjectMeta(name=storage_class),
                spec=client.V1PersistentVolumeClaimSpec(
                    storage_class_name=storage_class,
                    access_modes=["ReadWriteOnce"],
                    resources=client.V1VolumeResourceRequirements(
                        requests={"storage": "16Mi"}
                    ),
                ),
            ),
        )
        core_v1.create_namespaced_pod(
            namespace=namespace, body=consumer(storage_class, storage_class)
        )
    return time.monotonic() + PROVISION_TIMEOUT


@pytest.mark.kubernetes
@pytest.mark.parametrize("storage_class", STORAGE_CLASSES)
def test_claim_is_provisioned(
    core_v1: client.CoreV1Api, namespace: str, deadline: float, storage_class: str
) -> None:
    await_bound(core_v1, namespace, storage_class, deadline)
