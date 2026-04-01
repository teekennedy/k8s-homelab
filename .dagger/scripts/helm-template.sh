#!/bin/sh
set -e

SEARCH_PATHS="${SEARCH_PATHS:-k8s}"
CLUSTER_VALUES=""
if [ -f config/gen/cluster-values.yaml ]; then
    CLUSTER_VALUES="--values config/gen/cluster-values.yaml"
fi

# Register all non-OCI helm repositories referenced in Chart.yaml files.
# OCI repos (oci://) don't need registration.
echo "Registering helm repositories..."
REPO_INDEX=0
for chart_yaml in $(find $SEARCH_PATHS -name Chart.yaml -not -path "*/charts/*" | sort); do
    grep 'repository:' "$chart_yaml" | awk '{print $2}' | while read -r repo_url; do
        if [ -z "$repo_url" ] || echo "$repo_url" | grep -q '^oci://'; then
            continue
        fi
        # Use a hash of the URL as the repo name to deduplicate
        repo_name="repo-$(echo "$repo_url" | md5sum | cut -c1-8)"
        helm repo add "$repo_name" "$repo_url" 2>/dev/null || true
    done
done
helm repo update 2>/dev/null || true
echo ""

FAILED=0
RENDERED=0
ERRORS=""

for chart_yaml in $(find $SEARCH_PATHS -name Chart.yaml -not -path "*/charts/*" | sort); do
    chart_dir=$(dirname "$chart_yaml")
    app_yaml="$chart_dir/application.yaml"

    NAMESPACE=""
    RELEASE_NAME=$(basename "$chart_dir")
    if [ -f "$app_yaml" ]; then
        NAMESPACE=$(grep -A1 'destination:' "$app_yaml" | grep 'namespace:' | awk '{print $2}' | head -1)
        REL=$(grep 'releaseName:' "$app_yaml" | awk '{print $2}' | head -1)
        if [ -n "$REL" ]; then
            RELEASE_NAME="$REL"
        fi
    fi
    NAMESPACE="${NAMESPACE:-$RELEASE_NAME}"

    echo "=== Rendering $chart_dir (namespace: $NAMESPACE, release: $RELEASE_NAME) ==="

    if grep -q 'dependencies:' "$chart_yaml"; then
        echo "  Building dependencies..."
        if ! helm dependency build --skip-refresh "$chart_dir" 2>&1; then
            echo "  ERROR: Failed to build dependencies for $chart_dir"
            FAILED=$((FAILED + 1))
            ERRORS="$ERRORS\n  - $chart_dir: dependency build failed"
            continue
        fi
    fi

    if helm template "$RELEASE_NAME" "$chart_dir" \
        --namespace "$NAMESPACE" \
        $CLUSTER_VALUES \
        --debug 2>&1 | tail -5; then
        RENDERED=$((RENDERED + 1))
        echo "  OK"
    else
        FAILED=$((FAILED + 1))
        ERRORS="$ERRORS\n  - $chart_dir: helm template failed"
        echo "  FAILED"
    fi
    echo ""
done

echo "========================================="
echo "Results: $RENDERED rendered, $FAILED failed"
if [ $FAILED -gt 0 ]; then
    printf "Failures:%b\n" "$ERRORS"
    exit 1
fi
