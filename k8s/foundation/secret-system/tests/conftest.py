"""Fixtures for the secret-system integration tests.

These tests treat the secret generator as a black box: they create annotated
Secrets and watch for the values it is supposed to fill in. Nothing here knows
how it was deployed, so the same suite works against any cluster KUBECONFIG
names.

The fixtures that reach the cluster, and the conditions under which they refuse
to, are shared with the other releases' suites.
"""

import pytest
from homelab_testlib.namespaces import temporary_namespace
from kubernetes import client

pytest_plugins = ["homelab_testlib.fixtures"]


@pytest.fixture
def namespace(core_v1: client.CoreV1Api) -> str:
    """A namespace for one test, deleted afterwards whether or not it passed."""
    with temporary_namespace(
        core_v1, "secret-generator", "secret-system-tests"
    ) as name:
        yield name
