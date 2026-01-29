#!/bin/sh

set -e

# Determine if this is the master pod
POD_NAME="$(hostname)"

if [ "$POD_NAME" = "redis-0" ]; then
  echo "This is the redis master pod (redis-0)"
  SENTINEL_MASTER_HOST="127.0.0.1"
else
  echo "This is a redis replica pod ($POD_NAME)"
  # Use short hostname for Sentinel - it has stricter DNS requirements than nslookup
  SENTINEL_MASTER_HOST="redis-0.redis-sentinel-headless"
fi

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

echo "Redis config generated, verifying requirepass line..."
grep "^requirepass" /tmp/redis.conf | sed 's/requirepass .*/requirepass [REDACTED]/'

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
if [ "$POD_NAME" != "redis-0" ]; then
  echo "Waiting for redis master to be ready before starting Sentinel..."
  until getent hosts "$SENTINEL_MASTER_HOST" >/dev/null 2>&1; do
    echo "Still waiting for redis master DNS to be resolvable..."
    sleep 2
  done
  echo "redis-0 is resolvable"
fi

# Start Sentinel
redis-sentinel /tmp/sentinel.conf &
SENTINEL_PID=$!
echo "Sentinel started successfully"

# Wait for both processes
wait $REDIS_PID $SENTINEL_PID
