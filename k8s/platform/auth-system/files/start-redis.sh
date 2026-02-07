#!/bin/sh

set -e

# Determine if this is the master pod
POD_NAME="$(hostname)"
SENTINEL_MASTER_HOST="redis-0.redis-sentinel-headless"

# Generate Redis config with password
cat > /tmp/redis.conf <<EOF
bind 0.0.0.0
port 6379
requirepass ${REDIS_PASSWORD}
maxmemory 256mb
maxmemory-policy allkeys-lru
save 900 1
save 300 10
save 60 10000
appendonly no
protected-mode yes
EOF

echo "Generating Sentinel configuration..."
# Generate Sentinel config with password
cat > /tmp/sentinel.conf <<EOF
port 26379
requirepass ${REDIS_SENTINEL_PASSWORD}
sentinel resolve-hostnames yes
sentinel monitor mymaster ${SENTINEL_MASTER_HOST} 6379 2
sentinel down-after-milliseconds mymaster 5000
sentinel parallel-syncs mymaster 1
sentinel failover-timeout mymaster 10000
sentinel auth-pass mymaster ${REDIS_PASSWORD}
sentinel deny-scripts-reconfig yes
protected-mode no
EOF

# Start Redis server in background
redis-server /tmp/redis.conf &
REDIS_PID=$!

# Wait for Redis to be ready
until redis-cli -a "$REDIS_PASSWORD" --no-auth-warning ping 2>/dev/null; do
  echo "Waiting for Redis to start..."
  sleep 1
done
echo "Redis started successfully"

# Non-master pods wait for master DNS to be resolvable
echo "Waiting for redis master to be ready before starting Sentinel..."
until getent hosts "$SENTINEL_MASTER_HOST" >/dev/null 2>&1; do
  echo "Still waiting for redis master DNS to be resolvable..."
  sleep 2
done
echo "redis-0 is resolvable"

# Start Sentinel
redis-sentinel /tmp/sentinel.conf &
SENTINEL_PID=$!
echo "Sentinel started successfully"

# Wait for both processes
wait $REDIS_PID $SENTINEL_PID
