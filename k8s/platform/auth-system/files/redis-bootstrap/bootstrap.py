#!/usr/bin/env python3
"""
Redis Sentinel Bootstrap Script

Validates Redis Sentinel deployment by:
1. Connecting to Sentinel cluster
2. Discovering the Redis master
3. Testing authentication
4. Validating basic Redis operations
"""

import os
import sys
import time
from typing import Tuple

import redis
from redis.sentinel import Sentinel


def get_sentinel_connection() -> Sentinel:
    """Connect to Redis Sentinel cluster."""
    sentinel_host = os.environ.get("REDIS_SENTINEL_HOST", "redis-sentinel")
    sentinel_port = int(os.environ.get("REDIS_SENTINEL_PORT", "26379"))
    redis_password = os.environ.get("REDIS_PASSWORD")
    sentinel_password = os.environ.get("SENTINEL_PASSWORD")

    if not redis_password:
        raise ValueError("REDIS_PASSWORD environment variable is required")

    print(f"Connecting to Sentinel at {sentinel_host}:{sentinel_port}...")

    # Sentinel nodes for high availability
    sentinel_nodes = [
        (f"redis-{i}.redis-sentinel-headless", sentinel_port) for i in range(3)
    ]

    # Add the main sentinel service as well
    sentinel_nodes.insert(0, (sentinel_host, sentinel_port))

    sentinel = Sentinel(
        sentinel_nodes,
        socket_timeout=5,
        password=sentinel_password,
        sentinel_kwargs={"password": sentinel_password} if sentinel_password else {},
    )

    print("Successfully connected to Sentinel cluster")
    return sentinel


def discover_master(
    sentinel: Sentinel, master_name: str = "mymaster"
) -> Tuple[str, int]:
    """Discover the Redis master from Sentinel."""
    print(f"Discovering master '{master_name}' via Sentinel...")

    try:
        master_info = sentinel.discover_master(master_name)
        master_host, master_port = master_info
        print(f"Master discovered: {master_host}:{master_port}")
        return master_host, master_port
    except redis.sentinel.MasterNotFoundError as e:
        print(f"ERROR: Master '{master_name}' not found in Sentinel configuration")
        raise


def validate_operations(sentinel: Sentinel, master_name: str = "mymaster") -> bool:
    """Validate Redis operations through Sentinel."""
    redis_password = os.environ.get("REDIS_PASSWORD")

    print("Obtaining Redis master connection...")
    master = sentinel.master_for(
        master_name, socket_timeout=5, password=redis_password, decode_responses=True
    )

    print("Testing Redis operations...")

    # Test ping
    print("  - Testing PING...")
    response = master.ping()
    if not response:
        print("ERROR: PING failed")
        return False
    print("    ✓ PING successful")

    # Test set operation
    test_key = "bootstrap:test:key"
    test_value = f"bootstrap-test-{int(time.time())}"
    print(f"  - Testing SET {test_key}={test_value}...")
    master.set(test_key, test_value, ex=60)  # Expire in 60 seconds
    print("    ✓ SET successful")

    # Test get operation
    print(f"  - Testing GET {test_key}...")
    retrieved_value = master.get(test_key)
    if retrieved_value != test_value:
        print(f"ERROR: GET returned '{retrieved_value}', expected '{test_value}'")
        return False
    print(f"    ✓ GET successful (value: {retrieved_value})")

    # Test delete operation
    print(f"  - Testing DEL {test_key}...")
    master.delete(test_key)
    print("    ✓ DEL successful")

    # Verify deletion
    print(f"  - Verifying key deleted...")
    if master.get(test_key) is not None:
        print("ERROR: Key still exists after deletion")
        return False
    print("    ✓ Key successfully deleted")

    print("All Redis operations validated successfully!")
    return True


def main():
    """Main bootstrap function."""
    print("=" * 60)
    print("Redis Sentinel Bootstrap")
    print("=" * 60)

    try:
        # Step 1: Connect to Sentinel
        sentinel = get_sentinel_connection()

        # Step 2: Discover master
        master_host, master_port = discover_master(sentinel)

        # Step 3: Validate operations
        success = validate_operations(sentinel)

        if success:
            print("=" * 60)
            print("Bootstrap completed successfully!")
            print("=" * 60)
            return 0
        else:
            print("=" * 60)
            print("Bootstrap failed during validation")
            print("=" * 60)
            return 1

    except Exception as e:
        print(f"ERROR: Bootstrap failed with exception: {e}")
        import traceback

        traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())
