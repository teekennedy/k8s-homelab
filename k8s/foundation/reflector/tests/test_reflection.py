"""Black-box tests for reflector's ConfigMap mirroring."""

import time

import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException

# Reflector's annotation API (https://github.com/emberstack/kubernetes-reflector).
# `reflection-allowed*` is the source's consent to be copied; `reflection-auto*`
# is what makes it copy without a placeholder object in the target namespace.
ANNOTATION_PREFIX = "reflector.v1.k8s.emberstack.com"
ALLOWED = f"{ANNOTATION_PREFIX}/reflection-allowed"
ALLOWED_NAMESPACES = f"{ANNOTATION_PREFIX}/reflection-allowed-namespaces"
AUTO_ENABLED = f"{ANNOTATION_PREFIX}/reflection-auto-enabled"
AUTO_NAMESPACES = f"{ANNOTATION_PREFIX}/reflection-auto-namespaces"
# Stamped by reflector onto the copy, as "<namespace>/<name>" of the source.
REFLECTS = f"{ANNOTATION_PREFIX}/reflects"

# Reflector acts on a watch event, so a copy normally appears in well under a
# second. The workflow waits for the rollout before running these, so the
# ceiling only has to cover a slow reconcile, not a cold start.
REFLECTION_TIMEOUT = 30.0
POLL_INTERVAL = 0.5


def source_config_map(name: str, data: dict[str, str], target_ns: str):
    """A ConfigMap annotated to be kept mirrored into target_ns."""
    return client.V1ConfigMap(
        metadata=client.V1ObjectMeta(
            name=name,
            annotations={
                ALLOWED: "true",
                ALLOWED_NAMESPACES: target_ns,
                AUTO_ENABLED: "true",
                AUTO_NAMESPACES: target_ns,
            },
        ),
        data=data,
    )


def await_data(
    core_v1: client.CoreV1Api,
    namespace: str,
    name: str,
    expected: dict[str, str],
    description: str,
) -> client.V1ConfigMap:
    """Poll until namespace/name exists with exactly expected data, or fail.

    This is the assertion, not a prelude to one: returning means the contents
    matched, and timing out reports what was being waited for. A 404 counts as
    "not yet" — the normal state while waiting for an object reflector has not
    created — but any other API error propagates, so a broken RBAC rule fails
    the test instead of burning the whole timeout.
    """
    deadline = time.monotonic() + REFLECTION_TIMEOUT
    while True:
        try:
            config_map = core_v1.read_namespaced_config_map(
                name=name, namespace=namespace
            )
            if config_map.data == expected:
                return config_map
        except ApiException as exc:
            if exc.status != 404:
                raise
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"timed out after {REFLECTION_TIMEOUT:.0f}s waiting for {description}"
            )
        time.sleep(POLL_INTERVAL)


@pytest.mark.kubernetes
def test_configmap_is_reflected_into_the_target_namespace(
    core_v1: client.CoreV1Api, namespace_pair: tuple[str, str]
) -> None:
    source_ns, target_ns = namespace_pair
    name = "reflected-config"
    data = {"greeting": "hello from the source namespace"}

    core_v1.create_namespaced_config_map(
        namespace=source_ns, body=source_config_map(name, data, target_ns)
    )

    copy = await_data(
        core_v1,
        target_ns,
        name,
        data,
        f"reflector to copy {source_ns}/{name} into {target_ns}",
    )

    assert copy.metadata.annotations[REFLECTS] == f"{source_ns}/{name}"


@pytest.mark.kubernetes
def test_configmap_update_propagates_to_the_reflected_copy(
    core_v1: client.CoreV1Api, namespace_pair: tuple[str, str]
) -> None:
    source_ns, target_ns = namespace_pair
    name = "updated-config"
    original = {"greeting": "first value"}
    updated = {"greeting": "second value", "added": "new key"}

    core_v1.create_namespaced_config_map(
        namespace=source_ns, body=source_config_map(name, original, target_ns)
    )

    # Wait for the first copy before updating, so that a propagated update
    # cannot be mistaken for a slow initial reflection.
    await_data(
        core_v1,
        target_ns,
        name,
        original,
        f"reflector to copy {source_ns}/{name} into {target_ns}",
    )

    # `data` is replaced rather than merged: a strategic-merge patch of a map
    # field would leave the removed keys behind and weaken the assertion.
    core_v1.replace_namespaced_config_map(
        name=name,
        namespace=source_ns,
        body=source_config_map(name, updated, target_ns),
    )

    # Returning at all is the assertion: await_data only returns once the copy's
    # data matches `updated`, including the key that was not there before.
    await_data(
        core_v1,
        target_ns,
        name,
        updated,
        f"reflector to propagate the update to {target_ns}/{name}",
    )
