"""Fixtures for the cert-bundles integration tests.

These tests treat trust-manager and its bundles as a black box: they create a
namespace and look for the CA bundle that is supposed to appear in it. Nothing
here knows how the bundles were deployed, so the same suite works against any
cluster KUBECONFIG names.

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
    with temporary_namespace(core_v1, "cert-bundles", "cert-bundles-tests") as name:
        yield name
