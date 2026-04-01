#!/bin/sh
set -e

SEARCH_PATHS="${SEARCH_PATHS:-k8s}"
CLUSTER_VALUES=""
if [ -f config/gen/cluster-values.yaml ]; then
    CLUSTER_VALUES="--values config/gen/cluster-values.yaml"
fi

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
