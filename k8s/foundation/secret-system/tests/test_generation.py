"""Black-box tests for the secret generator's annotation-driven generation."""

import base64
import time
from collections.abc import Callable

import pytest
from kubernetes import client

# The generator's annotation API
# (https://github.com/mittwald/kubernetes-secret-generator).
ANNOTATION_PREFIX = "secret-generator.v1.mittwald.de"
# Comma-separated keys to fill in.
AUTOGENERATE = f"{ANNOTATION_PREFIX}/autogenerate"
LENGTH = f"{ANNOTATION_PREFIX}/length"
# Asks for the listed keys (or all of them, with "yes") to be generated again.
# The generator removes it once it has.
REGENERATE = f"{ANNOTATION_PREFIX}/regenerate"

# The generator acts on a watch event, so a value normally appears in well
# under a second. The workflow waits for the rollout before running these, so
# the ceiling only has to cover a slow reconcile, not a cold start.
GENERATION_TIMEOUT = 30.0
POLL_INTERVAL = 0.5


def annotated_secret(
    name: str, keys: str, length: int, data: dict[str, str] | None = None
) -> client.V1Secret:
    """A Secret asking for keys to be generated, length characters each."""
    return client.V1Secret(
        metadata=client.V1ObjectMeta(
            name=name, annotations={AUTOGENERATE: keys, LENGTH: str(length)}
        ),
        string_data=data,
    )


def decoded(secret: client.V1Secret) -> dict[str, str]:
    """The Secret's data with the API's base64 layer removed."""
    return {
        key: base64.b64decode(value).decode()
        for key, value in (secret.data or {}).items()
    }


def await_secret(
    core_v1: client.CoreV1Api,
    namespace: str,
    name: str,
    ready: Callable[[dict[str, str]], bool],
    description: str,
) -> dict[str, str]:
    """Poll until ready accepts namespace/name's decoded data, or fail.

    This is the assertion, not a prelude to one: returning means the data was
    accepted, and timing out reports what was being waited for. The Secret is
    created by the test, so any API error propagates rather than being retried.
    """
    deadline = time.monotonic() + GENERATION_TIMEOUT
    while True:
        data = decoded(core_v1.read_namespaced_secret(name=name, namespace=namespace))
        if ready(data):
            return data
        if time.monotonic() >= deadline:
            raise AssertionError(
                f"timed out after {GENERATION_TIMEOUT:.0f}s waiting for {description};"
                f" the Secret has keys {sorted(data)}"
            )
        time.sleep(POLL_INTERVAL)


@pytest.mark.kubernetes
def test_annotated_keys_are_generated(
    core_v1: client.CoreV1Api, namespace: str
) -> None:
    name = "generated"
    core_v1.create_namespaced_secret(
        namespace=namespace, body=annotated_secret(name, "password,token", 32)
    )

    data = await_secret(
        core_v1,
        namespace,
        name,
        lambda data: {"password", "token"} <= data.keys(),
        f"the generator to fill in password and token on {namespace}/{name}",
    )

    assert len(data["password"]) == 32
    assert len(data["token"]) == 32
    assert data["password"] != data["token"]


@pytest.mark.kubernetes
def test_keys_not_asked_for_are_left_alone(
    core_v1: client.CoreV1Api, namespace: str
) -> None:
    name = "partly-generated"
    core_v1.create_namespaced_secret(
        namespace=namespace,
        body=annotated_secret(name, "password", 24, data={"username": "by-hand"}),
    )

    data = await_secret(
        core_v1,
        namespace,
        name,
        lambda data: "password" in data,
        f"the generator to fill in password on {namespace}/{name}",
    )

    assert data["username"] == "by-hand"
    assert len(data["password"]) == 24


@pytest.mark.kubernetes
def test_regenerate_replaces_a_generated_value(
    core_v1: client.CoreV1Api, namespace: str
) -> None:
    name = "regenerated"
    core_v1.create_namespaced_secret(
        namespace=namespace, body=annotated_secret(name, "password", 32)
    )

    # Wait for the first value before asking for another, so that a slow first
    # generation cannot be mistaken for a regeneration.
    original = await_secret(
        core_v1,
        namespace,
        name,
        lambda data: "password" in data,
        f"the generator to fill in password on {namespace}/{name}",
    )["password"]

    core_v1.patch_namespaced_secret(
        name=name,
        namespace=namespace,
        body={"metadata": {"annotations": {REGENERATE: "yes"}}},
    )

    # Returning at all is the assertion: await_secret only returns once the
    # value differs from the one generated first.
    regenerated = await_secret(
        core_v1,
        namespace,
        name,
        lambda data: data.get("password") != original,
        f"the generator to replace password on {namespace}/{name}",
    )["password"]

    assert len(regenerated) == 32
