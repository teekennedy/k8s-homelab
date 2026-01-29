# Redis Sentinel Bootstrap

A Python-based bootstrap job that validates Redis Sentinel deployment for Authelia session management.

## Overview

This bootstrap script performs post-deployment validation of the Redis Sentinel cluster to ensure:

1. **Sentinel Connectivity**: All Sentinel nodes are reachable
2. **Master Discovery**: Sentinel can identify the current Redis master
3. **Authentication**: Redis password authentication works correctly
4. **Operations**: Basic Redis operations (PING, SET, GET, DEL) function properly

## Architecture

The bootstrap job runs as a Kubernetes Job with PostSync hooks, executing after Redis Sentinel StatefulSet deployment. It validates the complete Redis HA setup before Authelia starts using it for session storage.

### Bootstrap Process

```
┌─────────────────────────────────────────┐
│  1. Connect to Sentinel Cluster         │
│     - Primary service: redis-sentinel   │
│     - StatefulSet pods: redis-{0,1,2}   │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  2. Discover Redis Master               │
│     - Query Sentinel for master info    │
│     - Validate master is available      │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  3. Test Authentication                 │
│     - Connect using REDIS_PASSWORD      │
│     - Validate credentials work         │
└─────────────────┬───────────────────────┘
                  │
┌─────────────────▼───────────────────────┐
│  4. Validate Operations                 │
│     - PING: Test basic connectivity     │
│     - SET: Write test key with TTL      │
│     - GET: Read back test key           │
│     - DEL: Delete test key              │
│     - Verify deletion successful        │
└─────────────────────────────────────────┘
```

## Local Development

### Prerequisites

- Python 3.12+
- [uv](https://github.com/astral-sh/uv) package manager

### Setup

```bash
# Install uv if not already installed
curl -LsSf https://astral.sh/uv/install.sh | sh

# Sync dependencies
uv sync

# Or sync with dev dependencies
uv sync --all-groups
```

### Running Tests

```bash
# Run all tests
uv run pytest

# Run with verbose output
uv run pytest -v

# Run specific test file
uv run pytest tests/test_bootstrap.py

# Run with coverage
uv run pytest --cov=bootstrap --cov-report=term-missing
```

### Running Locally

The bootstrap script can be run locally for development:

```bash
# Set required environment variables
export REDIS_SENTINEL_HOST=localhost
export REDIS_SENTINEL_PORT=26379
export REDIS_PASSWORD=your-redis-password
export SENTINEL_PASSWORD=your-sentinel-password  # Optional

# Run the script
uv run python bootstrap.py
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `REDIS_SENTINEL_HOST` | No | `redis-sentinel` | Sentinel service hostname |
| `REDIS_SENTINEL_PORT` | No | `26379` | Sentinel service port |
| `REDIS_PASSWORD` | **Yes** | - | Redis authentication password |
| `SENTINEL_PASSWORD` | No | - | Sentinel authentication password (if enabled) |

## Kubernetes Deployment

The bootstrap job is deployed as part of the auth-system Helm chart:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  annotations:
    argocd.argoproj.io/hook: PostSync
    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation
    helm.sh/hook: post-install,post-upgrade
    helm.sh/hook-weight: "10"
spec:
  template:
    spec:
      serviceAccountName: redis-bootstrap
      containers:
      - name: bootstrap
        image: python:3.12-alpine
        env:
        - name: REDIS_PASSWORD
          valueFrom:
            secretKeyRef:
              name: authelia-secrets
              key: session.redis.password.txt
```

### RBAC

The job uses minimal RBAC permissions:

- **ServiceAccount**: `redis-bootstrap`
- **Role**: Read access to `authelia-secrets` secret
- **RoleBinding**: Binds ServiceAccount to Role

## Testing Strategy

### Unit Tests (`tests/test_bootstrap.py`)

Tests individual functions in isolation with mocked dependencies:

- Sentinel connection with various configurations
- Master discovery success and failure cases
- Redis operation validation
- Authentication with and without Sentinel password
- Error handling for network failures

### Integration Tests (`tests/test_integration.py`)

Tests complete workflows with mocked API sequences:

- Full bootstrap workflow from start to finish
- Sentinel failover scenarios
- Invalid credential handling
- Master not found errors
- Operations failing after successful connection

## Project Structure

```
redis-bootstrap/
├── bootstrap.py              # Main bootstrap script
├── pyproject.toml           # uv project configuration
├── .python-version          # Python version specification
├── .gitignore              # Git ignore patterns
├── uv.lock                 # Locked dependency versions
├── README.md               # This file
└── tests/
    ├── __init__.py
    ├── test_bootstrap.py    # Unit tests
    └── test_integration.py  # Integration tests
```

## Dependencies

- **redis>=5.0.0**: Python Redis client with Sentinel support
- **pytest>=9.0.2**: Testing framework (dev)
- **pytest-mock>=3.12.0**: Mocking utilities for pytest (dev)

## CI Integration

The bootstrap tests are integrated into the Dagger CI pipeline:

```go
func (m *Module) TestPyRedis(ctx context.Context, source *dagger.Directory) error {
    return dag.Container().
        From("python:3.12-alpine").
        WithDirectory("/src", source).
        WithWorkdir("/src/k8s/platform/auth-system/files/redis-bootstrap").
        WithExec([]string{"pip", "install", "uv"}).
        WithExec([]string{"uv", "sync"}).
        WithExec([]string{"uv", "run", "pytest", "tests/"}).
        Sync(ctx)
}
```

Run locally with:

```bash
lab ci all
```

## Success Criteria

The bootstrap job succeeds when:

1. ✓ Connection to Sentinel cluster established
2. ✓ Redis master discovered via Sentinel
3. ✓ Authentication with Redis password successful
4. ✓ PING command returns True
5. ✓ SET operation writes test key
6. ✓ GET operation retrieves correct value
7. ✓ DEL operation removes test key
8. ✓ Verification confirms key deleted

Exit code 0 indicates success, non-zero indicates failure.

## Troubleshooting

### Job Fails with "REDIS_PASSWORD environment variable is required"

Ensure the `authelia-secrets` Secret exists and contains `session.redis.password.txt` key.

### Job Fails with "Master 'mymaster' not found"

Check Redis Sentinel logs to ensure Sentinel is monitoring the master:

```bash
kubectl logs -n auth-system auth-system-redis-0 | grep sentinel
```

### Job Fails with Authentication Error

Verify the password in the secret matches the Redis configuration:

```bash
kubectl get secret -n auth-system authelia-secrets -o jsonpath='{.data.session\.redis\.password\.txt}' | base64 -d
```

### Connection Timeout

Check that Redis pods are running and healthy:

```bash
kubectl get pods -n auth-system -l app.kubernetes.io/component=redis
```

## License

Part of the k8s-homelab project.
