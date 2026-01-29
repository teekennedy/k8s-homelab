# CrowdSec Bouncer Middleware Bootstrap

This Python script automates the post-deployment setup of the CrowdSec bouncer middleware for Traefik.

## What It Does

1. **Checks** if the middleware already has a valid bouncer API key (idempotent)
2. **Generates** a new bouncer API key from CrowdSec LAPI if needed
3. **Updates** the Traefik middleware with the new key
4. **Restarts** Traefik to apply the changes

## Requirements

- Python 3.11+
- [uv](https://github.com/astral-sh/uv) for dependency management
- Access to a Kubernetes cluster with CrowdSec and Traefik installed

## Development

### Setup

Install dependencies:

```bash
uv sync
```

### Running Tests

Run unit tests (no cluster required):

```bash
uv run pytest -v
```

Run integration tests (requires cluster):

```bash
uv run pytest -v -m integration
```

### Formatting

Format code with black:

```bash
uv run black .
```

Check formatting:

```bash
uv run black --check .
```

## Deployment

This script is deployed as a Kubernetes Job via Helm chart. It runs as a post-install/post-upgrade hook.

See the parent Helm chart templates for:
- `bootstrap-job.yaml` - Job definition
- `bootstrap-rbac.yaml` - RBAC permissions
- `bootstrap-configmap.yaml` - Script configuration

## CI/CD Integration

The script is tested and linted via Dagger functions:

```bash
# Run tests
dagger call test-python --source=.

# Check formatting
dagger call lint-python --source=.

# Fix formatting
dagger call lint-python --source=. --fix=true
```

## Architecture

### RBAC Permissions

The job requires two RBAC resources:

1. **Role** (crowdsec namespace):
   - Read/update middlewares (traefik.io CRD)
   - Exec into LAPI pods to generate keys

2. **ClusterRole** (cross-namespace):
   - Restart Traefik deployment in kube-system namespace

### Idempotency

The script checks if the middleware already has a valid key before generating a new one. This prevents:
- Duplicate bouncer entries in CrowdSec
- Unnecessary Traefik restarts
- Slower subsequent runs

### Error Handling

The script exits with status 1 on errors:
- Middleware not found
- LAPI pod not available
- Failed to generate key
- Failed to update middleware
- Failed to restart Traefik

### Kubernetes API Usage

Uses the official Python Kubernetes client:
- `CustomObjectsApi` for Traefik CRDs
- `CoreV1Api` for pod operations
- `AppsV1Api` for deployment operations
- `stream` for pod exec

## Verification

After deployment, verify the bootstrap worked:

```bash
# Check job status
kubectl -n crowdsec get jobs
kubectl -n crowdsec logs job/crowdsec-bootstrap-middleware

# Verify bouncer is registered
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli bouncers list

# Verify middleware has real key
kubectl -n crowdsec get middleware crowdsec-bouncer -o yaml | grep CrowdsecLapiKey

# Test Traefik is using the bouncer
kubectl -n kube-system logs deployment/traefik | grep crowdsec
```
