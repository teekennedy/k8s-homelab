"""Black-box tests for reflector's ConfigMap mirroring."""

import time
from typing import Callable, Optional, TypeVar

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
# second. The ceiling is sized for a controller that has only just become ready.
REFLECTION_TIMEOUT = 90.0
POLL_INTERVAL = 0.5

T = TypeVar("T")


def wait_for(
    probe: Callable[[], Optional[T]],
    description: str,
    timeout: float = REFLECTION_TIMEOUT,
) -> T:
    """Poll probe until it returns a non-None value, or fail after timeout.

    A 404 from the API server counts as "not yet" — that is the normal state
    while waiting for an object reflector has not created. Any other API error
    propagates, so a broken RBAC rule fails the test instead of timing out.
    """
    deadline = time.monotonic() + timeout
    while True:
        try:
            result = probe()
        except ApiException as exc:
            if exc.status != 404:
                raise
            result = None
        if result is not None:
            return result
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"timed out after {timeout:.0f}s waiting for {description}"
            )
        time.sleep(POLL_INTERVAL)


def auto_reflect_into(namespace: str) -> dict[str, str]:
    """Annotations that ask reflector to keep a copy in namespace up to date."""
    return {
        ALLOWED: "true",
        ALLOWED_NAMESPACES: namespace,
        AUTO_ENABLED: "true",
        AUTO_NAMESPACES: namespace,
    }


def await_data(
    core_v1: client.CoreV1Api,
    namespace: str,
    name: str,
    expected: dict[str, str],
    description: str,
) -> client.V1ConfigMap:
    """Wait for namespace/name to exist with exactly the expected data."""
    return wait_for(
        lambda: _config_map_with_data(core_v1, namespace, name, expected),
        description,
    )


def _config_map_with_data(
    core_v1: client.CoreV1Api, namespace: str, name: str, expected: dict[str, str]
) -> Optional[client.V1ConfigMap]:
    config_map = core_v1.read_namespaced_config_map(name=name, namespace=namespace)
    return config_map if config_map.data == expected else None


@pytest.mark.kubernetes
def test_configmap_is_reflected_into_the_target_namespace(
    core_v1: client.CoreV1Api, namespace_pair: tuple[str, str]
) -> None:
    source_ns, target_ns = namespace_pair
    name = "reflected-config"
    data = {"greeting": "hello from the source namespace"}

    core_v1.create_namespaced_config_map(
        namespace=source_ns,
        body=client.V1ConfigMap(
            metadata=client.V1ObjectMeta(
                name=name, annotations=auto_reflect_into(target_ns)
            ),
            data=data,
        ),
    )

    copy = await_data(
        core_v1,
        target_ns,
        name,
        data,
        f"reflector to copy {source_ns}/{name} into {target_ns}",
    )

    assert copy.data == data
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
        namespace=source_ns,
        body=client.V1ConfigMap(
            metadata=client.V1ObjectMeta(
                name=name, annotations=auto_reflect_into(target_ns)
            ),
            data=original,
        ),
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
        body=client.V1ConfigMap(
            metadata=client.V1ObjectMeta(
                name=name, annotations=auto_reflect_into(target_ns)
            ),
            data=updated,
        ),
    )

    copy = await_data(
        core_v1,
        target_ns,
        name,
        updated,
        f"reflector to propagate the update to {target_ns}/{name}",
    )

    assert copy.data == updated
