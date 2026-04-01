#!/bin/sh
set -e

SEARCH_PATHS="${SEARCH_PATHS:-k8s}"

FAILED=0
LINTED=0

for chart_yaml in $(find $SEARCH_PATHS -name Chart.yaml -not -path "*/charts/*" | sort); do
    chart_dir=$(dirname "$chart_yaml")
    echo "Linting $chart_dir..."

    if helm lint "$chart_dir" 2>&1; then
        LINTED=$((LINTED + 1))
    else
        FAILED=$((FAILED + 1))
    fi
done

echo "Linted: $LINTED, Failed: $FAILED"
if [ $FAILED -gt 0 ]; then exit 1; fi
