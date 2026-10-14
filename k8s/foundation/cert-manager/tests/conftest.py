"""Fixtures for the cert-manager integration tests.

These tests treat cert-manager as a black box: they request a Certificate and
look for the Secret it is supposed to produce. Nothing here knows how it was
deployed, so the same suite works against any cluster KUBECONFIG names.

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
    with temporary_namespace(core_v1, "cert-manager", "cert-manager-tests") as name:
        yield name
