"""
Unit tests for the CrowdSec bootstrap script.

These tests use mocking to avoid requiring a Kubernetes cluster.
"""

import pytest
from unittest.mock import Mock, patch, MagicMock
from kubernetes.client.rest import ApiException
import sys
import os

# Add parent directory to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import bootstrap


class TestCheckMiddlewareConfigured:
    """Tests for check_middleware_configured function."""

    def test_middleware_already_configured(self):
        """Test when middleware has a valid API key."""
        custom_api = Mock()
        custom_api.get_namespaced_custom_object.return_value = {
            "spec": {
                "plugin": {
                    "crowdsec-bouncer": {"CrowdsecLapiKey": "valid-api-key-here"}
                }
            }
        }

        result = bootstrap.check_middleware_configured(custom_api)

        assert result is True
        custom_api.get_namespaced_custom_object.assert_called_once_with(
            group="traefik.io",
            version="v1alpha1",
            namespace="crowdsec",
            plural="middlewares",
            name="crowdsec-bouncer",
        )

    def test_middleware_has_placeholder(self):
        """Test when middleware has the placeholder key."""
        custom_api = Mock()
        custom_api.get_namespaced_custom_object.return_value = {
            "spec": {
                "plugin": {
                    "crowdsec-bouncer": {"CrowdsecLapiKey": "REPLACE_WITH_REAL_KEY"}
                }
            }
        }

        result = bootstrap.check_middleware_configured(custom_api)

        assert result is False

    def test_middleware_empty_key(self):
        """Test when middleware has an empty key."""
        custom_api = Mock()
        custom_api.get_namespaced_custom_object.return_value = {
            "spec": {"plugin": {"crowdsec-bouncer": {"CrowdsecLapiKey": ""}}}
        }

        result = bootstrap.check_middleware_configured(custom_api)

        assert result is False

    def test_middleware_not_found(self):
        """Test when middleware does not exist."""
        custom_api = Mock()
        exception = ApiException(status=404)
        custom_api.get_namespaced_custom_object.side_effect = exception

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.check_middleware_configured(custom_api)

        assert exc_info.value.code == 1


class TestGetLapiPod:
    """Tests for get_lapi_pod function."""

    def test_get_running_pod(self):
        """Test getting a running LAPI pod."""
        core_api = Mock()
        pod1 = Mock()
        pod1.metadata.name = "crowdsec-lapi-abc123"
        pod1.status.phase = "Running"

        pods_list = Mock()
        pods_list.items = [pod1]
        core_api.list_namespaced_pod.return_value = pods_list

        result = bootstrap.get_lapi_pod(core_api)

        assert result == "crowdsec-lapi-abc123"
        core_api.list_namespaced_pod.assert_called_once_with(
            namespace="crowdsec",
            label_selector="app.kubernetes.io/name=crowdsec,app.kubernetes.io/component=lapi",
        )

    def test_no_pods_found(self):
        """Test when no pods are found."""
        core_api = Mock()
        pods_list = Mock()
        pods_list.items = []
        core_api.list_namespaced_pod.return_value = pods_list

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.get_lapi_pod(core_api)

        assert exc_info.value.code == 1

    def test_no_running_pods(self):
        """Test when no running pods are found."""
        core_api = Mock()
        pod1 = Mock()
        pod1.metadata.name = "crowdsec-lapi-abc123"
        pod1.status.phase = "Pending"

        pods_list = Mock()
        pods_list.items = [pod1]
        core_api.list_namespaced_pod.return_value = pods_list

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.get_lapi_pod(core_api)

        assert exc_info.value.code == 1


class TestGenerateBouncerKey:
    """Tests for generate_bouncer_key function."""

    @patch("bootstrap.stream")
    def test_generate_key_success(self, mock_stream):
        """Test successful bouncer key generation."""
        core_api = Mock()
        mock_stream.stream.return_value = "generated-api-key-123\n"

        result = bootstrap.generate_bouncer_key(core_api, "crowdsec-lapi-abc123")

        assert result == "generated-api-key-123"
        mock_stream.stream.assert_called_once()

    @patch("bootstrap.stream")
    def test_generate_key_empty_response(self, mock_stream):
        """Test when bouncer key generation returns empty response."""
        core_api = Mock()
        mock_stream.stream.return_value = ""

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.generate_bouncer_key(core_api, "crowdsec-lapi-abc123")

        assert exc_info.value.code == 1


class TestUpdateMiddleware:
    """Tests for update_middleware function."""

    def test_update_success(self):
        """Test successful middleware update."""
        custom_api = Mock()
        api_key = "test-api-key-123"

        bootstrap.update_middleware(custom_api, api_key)

        custom_api.patch_namespaced_custom_object.assert_called_once_with(
            group="traefik.io",
            version="v1alpha1",
            namespace="crowdsec",
            plural="middlewares",
            name="crowdsec-bouncer",
            body={
                "spec": {"plugin": {"crowdsec-bouncer": {"CrowdsecLapiKey": api_key}}}
            },
        )

    def test_update_failure(self):
        """Test middleware update failure."""
        custom_api = Mock()
        custom_api.patch_namespaced_custom_object.side_effect = ApiException(status=500)

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.update_middleware(custom_api, "test-key")

        assert exc_info.value.code == 1


class TestRestartTraefik:
    """Tests for restart_traefik function."""

    @patch("bootstrap.time")
    def test_restart_success(self, mock_time):
        """Test successful Traefik restart."""
        mock_time.strftime.return_value = "20260129120000"
        mock_time.sleep = Mock()

        apps_api = Mock()

        # Mock deployment status - ready immediately
        deployment = Mock()
        deployment.spec.replicas = 2
        deployment.status.updated_replicas = 2
        deployment.status.ready_replicas = 2
        deployment.status.available_replicas = 2
        apps_api.read_namespaced_deployment.return_value = deployment

        bootstrap.restart_traefik(apps_api)

        apps_api.patch_namespaced_deployment.assert_called_once()
        args = apps_api.patch_namespaced_deployment.call_args
        assert args[1]["name"] == "traefik"
        assert args[1]["namespace"] == "kube-system"

    @patch("bootstrap.time")
    def test_restart_timeout(self, mock_time):
        """Test Traefik restart timeout (non-fatal)."""
        mock_time.strftime.return_value = "20260129120000"
        mock_time.sleep = Mock()

        apps_api = Mock()

        # Mock deployment status - never becomes ready
        deployment = Mock()
        deployment.spec.replicas = 2
        deployment.status.updated_replicas = 1
        deployment.status.ready_replicas = 1
        deployment.status.available_replicas = 1
        apps_api.read_namespaced_deployment.return_value = deployment

        # Should not raise, just print warning
        bootstrap.restart_traefik(apps_api)

        apps_api.patch_namespaced_deployment.assert_called_once()


class TestMain:
    """Tests for main function."""

    @patch("bootstrap.load_kubernetes_config")
    @patch("bootstrap.client")
    @patch("bootstrap.check_middleware_configured")
    def test_main_already_configured(self, mock_check, mock_client, mock_load_config):
        """Test main when middleware is already configured."""
        mock_check.return_value = True

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.main()

        assert exc_info.value.code == 0
        mock_load_config.assert_called_once()

    @patch("bootstrap.load_kubernetes_config")
    @patch("bootstrap.client")
    @patch("bootstrap.check_middleware_configured")
    @patch("bootstrap.get_lapi_pod")
    @patch("bootstrap.generate_bouncer_key")
    @patch("bootstrap.update_middleware")
    @patch("bootstrap.restart_traefik")
    def test_main_full_bootstrap(
        self,
        mock_restart,
        mock_update,
        mock_generate,
        mock_get_pod,
        mock_check,
        mock_client,
        mock_load_config,
    ):
        """Test main function with full bootstrap flow."""
        mock_check.return_value = False
        mock_get_pod.return_value = "crowdsec-lapi-abc123"
        mock_generate.return_value = "new-api-key-123"

        with pytest.raises(SystemExit) as exc_info:
            bootstrap.main()

        assert exc_info.value.code == 0
        mock_load_config.assert_called_once()
        mock_check.assert_called_once()
        mock_get_pod.assert_called_once()
        mock_generate.assert_called_once_with(
            mock_client.CoreV1Api(), "crowdsec-lapi-abc123"
        )
        mock_update.assert_called_once_with(
            mock_client.CustomObjectsApi(), "new-api-key-123"
        )
        mock_restart.assert_called_once()
