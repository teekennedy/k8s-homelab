"""The API clients every suite needs, bound to the cluster KUBECONFIG names.

A suite loads the fixtures by naming this module in its conftest's
`pytest_plugins`.

The suites do not try to work out for themselves whether the cluster they have
been pointed at is safe to write to — the `kubernetes` marker is what keeps
them from running where they weren't asked to. Once one has been selected, not
running is a failure: a missing kubeconfig, an unreachable API server or a
rejected credential raises rather than skipping.
"""

import os

import pytest
from kubernetes import client, config


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


@pytest.fixture(scope="session")
def custom_objects(core_v1: client.CoreV1Api) -> client.CustomObjectsApi:
    """A CustomObjectsApi on the client configuration core_v1 loaded."""
    return client.CustomObjectsApi()
