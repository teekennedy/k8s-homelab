"""Fixtures for the reflector integration tests.

These tests treat reflector as a black box: they create ConfigMaps, annotate
them, and watch for the copies reflector is supposed to make. Nothing here knows
how reflector was deployed, so the same suite works against any cluster
KUBECONFIG names.

The fixtures that reach the cluster, and the conditions under which they refuse
to, are shared with the other releases' suites.
"""

import pytest
from homelab_testlib.namespaces import temporary_namespace
from kubernetes import client

pytest_plugins = ["homelab_testlib.fixtures"]


@pytest.fixture
def namespace_pair(core_v1: client.CoreV1Api) -> tuple[str, str]:
    """A source and a target namespace for one test, deleted afterwards."""
    with (
        temporary_namespace(core_v1, "reflector-src", "reflector-tests") as source,
        temporary_namespace(core_v1, "reflector-dst", "reflector-tests") as target,
    ):
        yield source, target
