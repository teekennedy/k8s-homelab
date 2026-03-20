package helm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverCharts(t *testing.T) {
	tmp := t.TempDir()
	k8sDir := filepath.Join(tmp, "k8s")

	appDir := filepath.Join(k8sDir, "apps", "myapp")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "Chart.yaml"), []byte("apiVersion: v2\nname: myapp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "application.yaml"), []byte(`
spec:
  destination:
    namespace: my-namespace
`), 0o644))

	depDir := filepath.Join(appDir, "charts", "dep")
	require.NoError(t, os.MkdirAll(depDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(depDir, "Chart.yaml"), []byte("apiVersion: v2\nname: dep\n"), 0o644))

	charts, err := DiscoverCharts(k8sDir)
	require.NoError(t, err)
	require.Len(t, charts, 1)

	assert.Equal(t, "my-namespace", charts[0].Namespace)
	assert.Equal(t, "apps", charts[0].Tier)
	assert.Equal(t, "myapp", charts[0].Name)
	assert.Equal(t, "myapp", charts[0].ReleaseName)
}

func TestDiscoverChartsWithReleaseName(t *testing.T) {
	tmp := t.TempDir()
	k8sDir := filepath.Join(tmp, "k8s")

	appDir := filepath.Join(k8sDir, "foundation", "traefik")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "Chart.yaml"), []byte("apiVersion: v2\nname: traefik\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "application.yaml"), []byte(`
spec:
  destination:
    namespace: kube-system
  source:
    helm:
      releaseName: traefik
`), 0o644))

	charts, err := DiscoverCharts(k8sDir)
	require.NoError(t, err)
	require.Len(t, charts, 1)

	assert.Equal(t, "kube-system", charts[0].Namespace)
	assert.Equal(t, "traefik", charts[0].ReleaseName)
}

func TestParseChartInfoNoApplicationYaml(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte("apiVersion: v2\nname: standalone\n"), 0o644))

	info, err := ParseChartInfo(tmp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Base(tmp), info.Name)
	assert.Equal(t, info.Name, info.Namespace)
	assert.Equal(t, info.Name, info.ReleaseName)
}

func TestNeedsDependencyBuild_NoDeps(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte("apiVersion: v2\nname: nodeps\n"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestNeedsDependencyBuild_MissingChartsDir(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 1.0.0
    repository: https://example.com
`), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestNeedsDependencyBuild_MatchingTgz(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 1.0.0
    repository: https://example.com
`), 0o644))
	chartsDir := filepath.Join(tmp, "charts")
	require.NoError(t, os.MkdirAll(chartsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartsDir, "foo-1.0.0.tgz"), []byte("fake"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestNeedsDependencyBuild_VersionMismatch(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 2.0.0
    repository: https://example.com
`), 0o644))
	chartsDir := filepath.Join(tmp, "charts")
	require.NoError(t, os.MkdirAll(chartsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartsDir, "foo-1.0.0.tgz"), []byte("fake"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestNeedsDependencyBuild_StaleTgz(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 1.0.0
    repository: https://example.com
`), 0o644))
	chartsDir := filepath.Join(tmp, "charts")
	require.NoError(t, os.MkdirAll(chartsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(chartsDir, "foo-1.0.0.tgz"), []byte("fake"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(chartsDir, "bar-1.0.0.tgz"), []byte("stale"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestNeedsDependencyBuild_ChartLockNewerThanTgz(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 1.0.0
    repository: https://example.com
`), 0o644))
	chartsDir := filepath.Join(tmp, "charts")
	require.NoError(t, os.MkdirAll(chartsDir, 0o755))

	tgzPath := filepath.Join(chartsDir, "foo-1.0.0.tgz")
	require.NoError(t, os.WriteFile(tgzPath, []byte("fake"), 0o644))
	oldTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(tgzPath, oldTime, oldTime))

	lockPath := filepath.Join(tmp, "Chart.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("digest: sha256:abc123\n"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestNeedsDependencyBuild_ChartLockOlderThanTgz(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "Chart.yaml"), []byte(`
apiVersion: v2
dependencies:
  - name: foo
    version: 1.0.0
    repository: https://example.com
`), 0o644))
	chartsDir := filepath.Join(tmp, "charts")
	require.NoError(t, os.MkdirAll(chartsDir, 0o755))

	lockPath := filepath.Join(tmp, "Chart.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("digest: sha256:abc123\n"), 0o644))
	oldTime := time.Now().Add(-1 * time.Hour)
	require.NoError(t, os.Chtimes(lockPath, oldTime, oldTime))

	require.NoError(t, os.WriteFile(filepath.Join(chartsDir, "foo-1.0.0.tgz"), []byte("fake"), 0o644))

	needs, err := NeedsDependencyBuild(tmp)
	require.NoError(t, err)
	assert.False(t, needs)
}
