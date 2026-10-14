"""Black-box tests for provisioning from the aliased storage classes."""

import time

import pytest
from kubernetes import client

# One class per storage release the aliases stand in for.
STORAGE_CLASSES = ["longhorn", "longhorn-strict-local", "nfs", "smb"]

# Provisioning runs a helper pod, so this has to cover an image pull on a
# cluster that has never provisioned a volume before. Each class gets this long
# from when its own test starts waiting, however long the classes before it took.
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


def claim(core_v1: client.CoreV1Api, namespace: str, storage_class: str) -> None:
    """Claim a volume from storage_class, and start a pod that consumes it."""
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


def await_bound(
    core_v1: client.CoreV1Api, namespace: str, claim: str, claimed_at: float
) -> None:
    """Poll until the claim is Bound, or fail with the phase it was left in.

    This is the assertion, not a prelude to one.
    """
    deadline = time.monotonic() + PROVISION_TIMEOUT
    while True:
        phase = core_v1.read_namespaced_persistent_volume_claim(
            name=claim, namespace=namespace
        ).status.phase
        if phase == "Bound":
            return
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"{namespace}/{claim} is still {phase} "
                f"{time.monotonic() - claimed_at:.0f}s after it was claimed"
            )
        time.sleep(POLL_INTERVAL)


@pytest.fixture(scope="module")
def claimed(
    request: pytest.FixtureRequest, core_v1: client.CoreV1Api, namespace: str
) -> dict[str, float | Exception]:
    """Claim a volume from each class under test, and return when each was.

    Claimed together so the classes provision side by side and the waits
    overlap, instead of each test paying for its own. A class that could not be
    claimed maps to the error instead, so that it fails its own test and not
    the others'.
    """
    # Only the classes whose tests were selected, so `-k` provisions no more
    # than it asked for.
    selected = {
        item.callspec.params["storage_class"]
        for item in request.session.items
        if item.module is request.module and hasattr(item, "callspec")
    }
    outcomes: dict[str, float | Exception] = {}
    for storage_class in STORAGE_CLASSES:
        if storage_class not in selected:
            continue
        try:
            claim(core_v1, namespace, storage_class)
        except Exception as exc:  # noqa: BLE001 - re-raised by the class's test
            outcomes[storage_class] = exc
        else:
            outcomes[storage_class] = time.monotonic()
    return outcomes


@pytest.mark.kubernetes
@pytest.mark.parametrize("storage_class", STORAGE_CLASSES)
def test_claim_is_provisioned(
    core_v1: client.CoreV1Api,
    namespace: str,
    claimed: dict[str, float | Exception],
    storage_class: str,
) -> None:
    outcome = claimed[storage_class]
    if isinstance(outcome, Exception):
        raise outcome
    await_bound(core_v1, namespace, storage_class, claimed_at=outcome)
