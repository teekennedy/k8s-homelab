"""
Integration tests for the CrowdSec bootstrap script.

These tests require an actual Kubernetes cluster with CrowdSec installed.
They are skipped by default and can be run with: pytest -m integration
"""

import pytest
import sys
import os

# Add parent directory to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import bootstrap
from kubernetes import client, config


@pytest.fixture(scope="module")
def k8s_clients():
    """
    Load Kubernetes configuration and create API clients.

    This fixture is skipped if no cluster is available.
    """
    try:
        config.load_kube_config()
    except config.ConfigException:
        pytest.skip("Kubernetes cluster not available")

    return {
        "core": client.CoreV1Api(),
        "apps": client.AppsV1Api(),
        "custom": client.CustomObjectsApi(),
    }


@pytest.mark.integration
def test_middleware_exists(k8s_clients):
    """Test that the middleware resource exists."""
    custom_api = k8s_clients["custom"]

    try:
        middleware = custom_api.get_namespaced_custom_object(
            group="traefik.io",
            version="v1alpha1",
            namespace=bootstrap.CROWDSEC_NAMESPACE,
            plural="middlewares",
            name=bootstrap.MIDDLEWARE_NAME,
        )
        assert middleware is not None
        assert "spec" in middleware
    except Exception as e:
        pytest.fail(f"Failed to get middleware: {e}")


@pytest.mark.integration
def test_lapi_deployment_exists(k8s_clients):
    """Test that the LAPI deployment exists and has running pods."""
    core_api = k8s_clients["core"]

    pods = core_api.list_namespaced_pod(
        namespace=bootstrap.CROWDSEC_NAMESPACE,
        label_selector="app.kubernetes.io/name=crowdsec,app.kubernetes.io/component=lapi",
    )

    assert len(pods.items) > 0, "No LAPI pods found"

    running_pods = [pod for pod in pods.items if pod.status.phase == "Running"]
    assert len(running_pods) > 0, "No running LAPI pods found"


@pytest.mark.integration
def test_traefik_deployment_exists(k8s_clients):
    """Test that the Traefik deployment exists."""
    apps_api = k8s_clients["apps"]

    try:
        deployment = apps_api.read_namespaced_deployment(
            name=bootstrap.TRAEFIK_DEPLOYMENT,
            namespace=bootstrap.TRAEFIK_NAMESPACE,
        )
        assert deployment is not None
        assert deployment.status.ready_replicas > 0
    except Exception as e:
        pytest.fail(f"Failed to get Traefik deployment: {e}")


@pytest.mark.integration
def test_check_middleware_configured_integration(k8s_clients):
    """Test the check_middleware_configured function against a real cluster."""
    custom_api = k8s_clients["custom"]

    result = bootstrap.check_middleware_configured(custom_api)
    assert isinstance(result, bool)


@pytest.mark.integration
def test_get_lapi_pod_integration(k8s_clients):
    """Test the get_lapi_pod function against a real cluster."""
    core_api = k8s_clients["core"]

    pod_name = bootstrap.get_lapi_pod(core_api)
    assert isinstance(pod_name, str)
    assert len(pod_name) > 0
