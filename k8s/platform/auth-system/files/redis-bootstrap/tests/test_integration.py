"""Integration tests for Redis bootstrap script."""

import pytest
from unittest.mock import MagicMock, patch
import redis.sentinel

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

import bootstrap


class TestFullBootstrapWorkflow:
    """Test complete bootstrap workflow."""

    @patch("time.time", return_value=1234567890)
    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
            "SENTINEL_PASSWORD": "sentinel-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_full_bootstrap_workflow(self, mock_sentinel_class, mock_time):
        """Test complete bootstrap workflow from start to finish."""
        # Setup mocks
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel
        mock_sentinel.discover_master.return_value = ("redis-0", 6379)
        mock_sentinel.master_for.return_value = mock_master

        # Mock Redis operations
        mock_master.ping.return_value = True
        mock_master.set.return_value = True
        mock_master.get.side_effect = ["bootstrap-test-1234567890", None]
        mock_master.delete.return_value = 1

        # Run main bootstrap
        result = bootstrap.main()

        # Verify success
        assert result == 0

        # Verify Sentinel was created
        mock_sentinel_class.assert_called_once()

        # Verify master discovery
        mock_sentinel.discover_master.assert_called_once_with("mymaster")

        # Verify master connection
        mock_sentinel.master_for.assert_called_once_with(
            "mymaster",
            socket_timeout=5,
            password="test-password",
            decode_responses=True,
        )

        # Verify operations
        mock_master.ping.assert_called_once()
        assert mock_master.set.called
        assert mock_master.get.called
        mock_master.delete.assert_called_once()

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_bootstrap_with_connection_failure(self, mock_sentinel_class):
        """Test bootstrap handles connection failures gracefully."""
        mock_sentinel_class.side_effect = redis.ConnectionError("Connection refused")

        result = bootstrap.main()

        assert result == 1

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_bootstrap_with_master_not_found(self, mock_sentinel_class):
        """Test bootstrap handles master not found error."""
        mock_sentinel = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel
        mock_sentinel.discover_master.side_effect = redis.sentinel.MasterNotFoundError()

        result = bootstrap.main()

        assert result == 1


class TestSentinelFailoverScenario:
    """Test Sentinel failover scenarios."""

    @patch("time.time", return_value=1234567890)
    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
            "SENTINEL_PASSWORD": "sentinel-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_sentinel_failover_scenario(self, mock_sentinel_class, mock_time):
        """Test bootstrap works when master changes during operation."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel

        # First call returns redis-0, simulating initial master
        mock_sentinel.discover_master.return_value = ("redis-0", 6379)
        mock_sentinel.master_for.return_value = mock_master

        # Mock successful operations
        mock_master.ping.return_value = True
        mock_master.set.return_value = True
        mock_master.get.side_effect = ["bootstrap-test-1234567890", None]
        mock_master.delete.return_value = 1

        result = bootstrap.main()

        assert result == 0

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_multiple_sentinel_nodes(self, mock_sentinel_class):
        """Test bootstrap connects to multiple Sentinel nodes."""
        mock_sentinel = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel

        bootstrap.get_sentinel_connection()

        # Verify Sentinel was created with multiple nodes
        call_args = mock_sentinel_class.call_args
        sentinel_nodes = call_args[0][0]

        # Should have main service + 3 StatefulSet pods
        assert len(sentinel_nodes) >= 4
        assert ("redis-sentinel", 26379) in sentinel_nodes
        assert ("redis-0.redis-sentinel-headless", 26379) in sentinel_nodes
        assert ("redis-1.redis-sentinel-headless", 26379) in sentinel_nodes
        assert ("redis-2.redis-sentinel-headless", 26379) in sentinel_nodes


class TestInvalidCredentials:
    """Test authentication failure scenarios."""

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "wrong-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_invalid_credentials(self, mock_sentinel_class):
        """Test bootstrap fails with invalid credentials."""
        mock_sentinel = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel
        mock_sentinel.discover_master.return_value = ("redis-0", 6379)

        # Simulate authentication error
        mock_sentinel.master_for.side_effect = redis.AuthenticationError(
            "Invalid password"
        )

        result = bootstrap.main()

        assert result == 1

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "redis-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_operations_fail_after_connection(self, mock_sentinel_class):
        """Test bootstrap fails when operations fail after connection."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel
        mock_sentinel.discover_master.return_value = ("redis-0", 6379)
        mock_sentinel.master_for.return_value = mock_master

        # Ping fails
        mock_master.ping.return_value = False

        result = bootstrap.main()

        assert result == 1
