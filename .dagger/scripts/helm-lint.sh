#!/bin/sh
set -e

# Register all non-OCI helm repositories
echo "Registering helm repositories..."
for chart_yaml in $(find k8s -name Chart.yaml -not -path "*/charts/*" | sort); do
    grep 'repository:' "$chart_yaml" | awk '{print $2}' | while read -r repo_url; do
        if [ -z "$repo_url" ] || echo "$repo_url" | grep -q '^oci://'; then
            continue
        fi
        repo_name="repo-$(echo "$repo_url" | md5sum | cut -c1-8)"
        helm repo add "$repo_name" "$repo_url" 2>/dev/null || true
    done
done
helm repo update 2>/dev/null || true
echo ""

FAILED=0
LINTED=0

for chart_yaml in $(find k8s -name Chart.yaml -not -path "*/charts/*" | sort); do
    chart_dir=$(dirname "$chart_yaml")
    echo "Linting $chart_dir..."

    if grep -q 'dependencies:' "$chart_yaml"; then
        helm dependency build "$chart_dir" 2>/dev/null || true
    fi

    if helm lint "$chart_dir" 2>&1; then
        LINTED=$((LINTED + 1))
    else
        FAILED=$((FAILED + 1))
    fi
done

echo "Linted: $LINTED, Failed: $FAILED"
if [ $FAILED -gt 0 ]; then exit 1; fi
