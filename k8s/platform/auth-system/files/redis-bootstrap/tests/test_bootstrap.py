"""Unit tests for Redis bootstrap script."""

import pytest
from unittest.mock import MagicMock, patch, call
import redis.sentinel

import sys
import os

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

import bootstrap


class TestSentinelConnection:
    """Test Sentinel connection functionality."""

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "test-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "test-password",
            "SENTINEL_PASSWORD": "sentinel-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_sentinel_connection_success(self, mock_sentinel_class):
        """Test successful Sentinel connection."""
        mock_sentinel = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel

        result = bootstrap.get_sentinel_connection()

        assert result == mock_sentinel
        mock_sentinel_class.assert_called_once()
        call_args = mock_sentinel_class.call_args

        # Verify sentinel nodes
        sentinel_nodes = call_args[0][0]
        assert ("test-sentinel", 26379) in sentinel_nodes
        assert ("redis-0.redis-sentinel-headless", 26379) in sentinel_nodes
        assert ("redis-1.redis-sentinel-headless", 26379) in sentinel_nodes
        assert ("redis-2.redis-sentinel-headless", 26379) in sentinel_nodes

        # Verify kwargs
        kwargs = call_args[1]
        assert kwargs["socket_timeout"] == 5
        assert kwargs["password"] == "sentinel-password"

    @patch.dict(os.environ, {}, clear=True)
    def test_connection_missing_password(self):
        """Test connection fails without REDIS_PASSWORD."""
        with pytest.raises(
            ValueError, match="REDIS_PASSWORD environment variable is required"
        ):
            bootstrap.get_sentinel_connection()


class TestMasterDiscovery:
    """Test master discovery functionality."""

    def test_discover_master_success(self):
        """Test successful master discovery."""
        mock_sentinel = MagicMock()
        mock_sentinel.discover_master.return_value = ("redis-master", 6379)

        host, port = bootstrap.discover_master(mock_sentinel, "mymaster")

        assert host == "redis-master"
        assert port == 6379
        mock_sentinel.discover_master.assert_called_once_with("mymaster")

    def test_discover_master_not_found(self):
        """Test master not found error."""
        mock_sentinel = MagicMock()
        mock_sentinel.discover_master.side_effect = redis.sentinel.MasterNotFoundError()

        with pytest.raises(redis.sentinel.MasterNotFoundError):
            bootstrap.discover_master(mock_sentinel, "mymaster")


class TestRedisOperations:
    """Test Redis operations validation."""

    @patch("time.time", return_value=1234567890)
    @patch.dict(os.environ, {"REDIS_PASSWORD": "test-password"})
    def test_validate_operations_success(self, mock_time):
        """Test successful Redis operations validation."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel.master_for.return_value = mock_master

        # Mock Redis responses
        mock_master.ping.return_value = True
        mock_master.set.return_value = True
        mock_master.get.side_effect = [
            "bootstrap-test-1234567890",
            None,
        ]  # First get returns value, second returns None
        mock_master.delete.return_value = 1

        result = bootstrap.validate_operations(mock_sentinel, "mymaster")

        assert result is True
        mock_sentinel.master_for.assert_called_once_with(
            "mymaster",
            socket_timeout=5,
            password="test-password",
            decode_responses=True,
        )
        mock_master.ping.assert_called_once()
        assert mock_master.set.called
        assert mock_master.get.call_count == 2
        mock_master.delete.assert_called_once()

    @patch.dict(os.environ, {"REDIS_PASSWORD": "test-password"})
    def test_validate_operations_ping_fails(self):
        """Test validation fails when ping fails."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel.master_for.return_value = mock_master

        mock_master.ping.return_value = False

        result = bootstrap.validate_operations(mock_sentinel, "mymaster")

        assert result is False

    @patch.dict(os.environ, {"REDIS_PASSWORD": "test-password"})
    def test_validate_operations_get_mismatch(self):
        """Test validation fails when GET returns wrong value."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel.master_for.return_value = mock_master

        mock_master.ping.return_value = True
        mock_master.set.return_value = True
        mock_master.get.return_value = "wrong-value"

        result = bootstrap.validate_operations(mock_sentinel, "mymaster")

        assert result is False

    @patch.dict(os.environ, {"REDIS_PASSWORD": "test-password"})
    def test_validate_operations_delete_fails(self):
        """Test validation fails when delete verification fails."""
        mock_sentinel = MagicMock()
        mock_master = MagicMock()
        mock_sentinel.master_for.return_value = mock_master

        mock_master.ping.return_value = True
        mock_master.set.return_value = True
        mock_master.get.side_effect = ["bootstrap-test-123", "still-exists"]
        mock_master.delete.return_value = 1

        result = bootstrap.validate_operations(mock_sentinel, "mymaster")

        assert result is False


class TestAuthentication:
    """Test authentication handling."""

    @patch.dict(
        os.environ,
        {
            "REDIS_SENTINEL_HOST": "test-sentinel",
            "REDIS_SENTINEL_PORT": "26379",
            "REDIS_PASSWORD": "correct-password",
        },
    )
    @patch("bootstrap.Sentinel")
    def test_authentication_no_sentinel_password(self, mock_sentinel_class):
        """Test authentication works without Sentinel password."""
        mock_sentinel = MagicMock()
        mock_sentinel_class.return_value = mock_sentinel

        result = bootstrap.get_sentinel_connection()

        call_args = mock_sentinel_class.call_args
        kwargs = call_args[1]
        assert kwargs["password"] is None
        assert "sentinel_kwargs" not in kwargs or kwargs["sentinel_kwargs"] == {}
