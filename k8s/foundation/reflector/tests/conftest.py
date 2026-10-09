"""Fixtures for the reflector integration tests.

These tests treat reflector as a black box: they create ConfigMaps, annotate
them, and watch for the copies reflector is supposed to make. Nothing here knows
how reflector was deployed, so the same suite works against the ephemeral k3d
cluster the Dagger workflow builds and against any other cluster KUBECONFIG
names.

The suite does not try to work out for itself whether the cluster it has been
pointed at is safe to write to — the `kubernetes` marker is what keeps it from
running where it wasn't asked to. Once it has been selected, not running is a
failure: a missing kubeconfig, an unreachable API server or a rejected
credential raises rather than skipping.
"""

import os
import uuid

import pytest
from kubernetes import client, config
from kubernetes.client.rest import ApiException


def _kubeconfig_path() -> str:
    """Return the kubeconfig KUBECONFIG names, or raise saying why it can't."""
    path = os.environ.get("KUBECONFIG", "").strip()
    if not path:
        raise RuntimeError(
            "KUBECONFIG is not set. These tests only talk to the cluster "
            "KUBECONFIG names; there is deliberately no fallback to "
            "~/.kube/config or to in-cluster credentials."
        )
    # KUBECONFIG is a path list, which the client merges itself. Only the first
    # entry is checked here, so a typo fails with the path rather than with a
    # parse error from deep inside the client.
    first = path.split(os.pathsep)[0]
    if not os.path.isfile(first):
        raise RuntimeError(f"KUBECONFIG names {first}, which is not a file")
    return path


@pytest.fixture(scope="session")
def core_v1() -> client.CoreV1Api:
    """A CoreV1Api bound to KUBECONFIG, proven to reach the API server."""
    kubeconfig = _kubeconfig_path()
    config.load_kube_config(config_file=kubeconfig)
    api = client.CoreV1Api()
    try:
        api.list_namespace(limit=1)
    except Exception as exc:  # noqa: BLE001 - the cause is what matters here
        raise RuntimeError(
            f"cannot list namespaces with the credentials in {kubeconfig}: {exc}"
        ) from exc
    return api


@pytest.fixture
def namespace_pair(core_v1: client.CoreV1Api) -> tuple[str, str]:
    """Create a source and a target namespace, and delete them afterwards.

    Names carry a random suffix so concurrent runs — or a previous run whose
    namespaces are still terminating — cannot collide.
    """
    suffix = uuid.uuid4().hex[:8]
    names = (f"reflector-src-{suffix}", f"reflector-dst-{suffix}")

    created = []
    try:
        for name in names:
            core_v1.create_namespace(
                client.V1Namespace(
                    metadata=client.V1ObjectMeta(
                        name=name,
                        labels={"app.kubernetes.io/managed-by": "reflector-tests"},
                    )
                )
            )
            created.append(name)
        yield names
    finally:
        # Teardown runs whether or not the assertions passed. Deletion is
        # asynchronous; the namespaces are left Terminating rather than waited
        # on, because every run uses fresh names.
        for name in created:
            try:
                core_v1.delete_namespace(name)
            except ApiException as exc:
                if exc.status != 404:
                    raise
