"""Fixtures for the storage-aliases integration tests.

These tests treat the aliased storage classes as a black box: they claim a
volume from each and watch for it to be provisioned. Nothing here knows how the
classes were deployed, so the same suite works against any cluster KUBECONFIG
names.

The fixtures that reach the cluster, and the conditions under which they refuse
to, are shared with the other releases' suites.
"""

import pytest
from homelab_testlib.namespaces import temporary_namespace
from kubernetes import client

pytest_plugins = ["homelab_testlib.fixtures"]


@pytest.fixture(scope="module")
def namespace(core_v1: client.CoreV1Api) -> str:
    """A namespace for one test module, deleted afterwards whether or not it passed."""
    with temporary_namespace(
        core_v1, "storage-aliases", "storage-aliases-tests"
    ) as name:
        yield name
