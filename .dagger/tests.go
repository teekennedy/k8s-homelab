package main

import (
	"context"
	"fmt"
)

// TestBuildHelm is an integration test that verifies BuildHelm can discover and render charts.
func (m *Homelab) TestBuildHelm(ctx context.Context) (string, error) {
	source := dag.Directory().
		WithNewFile("k8s/apps/test-app/Chart.yaml", `apiVersion: v2
name: test-app
version: 0.1.0
`).
		WithNewFile("k8s/apps/test-app/application.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: test-app
  namespace: argocd
spec:
  destination:
    namespace: test-ns
`).
		WithNewFile("k8s/apps/test-app/templates/configmap.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-app
data:
  key: value
`)

	result, err := m.BuildHelm(ctx, source, nil)
	if err != nil {
		return "", fmt.Errorf("BuildHelm integration test failed: %w", err)
	}

	return "BuildHelm integration test passed: " + result, nil
}
