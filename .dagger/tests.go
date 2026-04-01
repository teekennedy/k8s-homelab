package main

import (
	"context"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// testChartSource returns a minimal Helm chart source directory for testing.
func testChartSource() *dagger.Directory {
	return dag.Directory().
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
}

// testChartWithDepsSource returns a Helm chart source with a dependency for testing
// the shared dependency build step.
func testChartWithDepsSource() *dagger.Directory {
	return dag.Directory().
		WithNewFile("k8s/apps/dep-app/Chart.yaml", `apiVersion: v2
name: dep-app
version: 0.1.0
dependencies:
  - name: app-template
    version: 4.0.1
    repository: oci://ghcr.io/bjw-s-labs/helm
`).
		WithNewFile("k8s/apps/dep-app/values.yaml", `app-template:
  controllers:
    main:
      containers:
        main:
          image:
            repository: nginx
            tag: latest
`)
}

// TestBuildHelm is an integration test that verifies BuildHelm can discover and render charts.
func (m *Homelab) TestBuildHelm(ctx context.Context) (string, error) {
	source := testChartSource()

	result, err := m.BuildHelm(ctx, source, nil)
	if err != nil {
		return "", fmt.Errorf("BuildHelm integration test failed: %w", err)
	}

	return "BuildHelm integration test passed: " + result, nil
}

// TestValidateHelm is an integration test that verifies ValidateHelm can lint charts.
func (m *Homelab) TestValidateHelm(ctx context.Context) (string, error) {
	source := testChartSource()

	result, err := m.ValidateHelm(ctx, source, nil)
	if err != nil {
		return "", fmt.Errorf("ValidateHelm integration test failed: %w", err)
	}

	return "ValidateHelm integration test passed: " + result, nil
}

// TestBuildHelmWithDeps verifies the shared dependency build step works
// by rendering a chart that has an OCI dependency.
func (m *Homelab) TestBuildHelmWithDeps(ctx context.Context) (string, error) {
	source := testChartWithDepsSource()

	result, err := m.BuildHelm(ctx, source, nil)
	if err != nil {
		return "", fmt.Errorf("BuildHelmWithDeps integration test failed: %w", err)
	}

	return "BuildHelmWithDeps integration test passed: " + result, nil
}
