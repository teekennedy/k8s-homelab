package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	// Test with default XDG paths
	mgr := NewManager()
	if mgr.stateDir == "" {
		t.Error("expected stateDir to be set")
	}
	if mgr.configDir == "" {
		t.Error("expected configDir to be set")
	}
}

func TestNewManagerWithOptions(t *testing.T) {
	mgr := NewManager(
		WithStateDir("/custom/state"),
		WithConfigDir("/custom/config"),
	)
	if mgr.stateDir != "/custom/state" {
		t.Errorf("expected stateDir to be /custom/state, got %s", mgr.stateDir)
	}
	if mgr.configDir != "/custom/config" {
		t.Errorf("expected configDir to be /custom/config, got %s", mgr.configDir)
	}
}

func TestGetStatePath(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))
	expected := "/state/staging/state.json"
	got := mgr.getStatePath("staging")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestGetKubeconfigPath(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))
	expected := "/state/staging/kubeconfig"
	got := mgr.getKubeconfigPath("staging")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestGetKindConfigPath(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))
	expected := "/state/staging/kind-config.yaml"
	got := mgr.getKindConfigPath("staging")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestSaveAndLoadState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	env := &Environment{
		Name:   "test",
		Type:   TypeKind,
		Status: StatusRunning,
		Config: EnvConfig{
			KindClusterName: "lab-test",
			Workers:         2,
		},
	}

	// Save state
	if err := mgr.saveState(env); err != nil {
		t.Fatalf("save state failed: %v", err)
	}

	// Verify state file exists
	statePath := mgr.getStatePath("test")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("state file not created")
	}

	// Load state
	loaded, err := mgr.loadState("test")
	if err != nil {
		t.Fatalf("load state failed: %v", err)
	}

	if loaded.Name != env.Name {
		t.Errorf("expected name %s, got %s", env.Name, loaded.Name)
	}
	if loaded.Type != env.Type {
		t.Errorf("expected type %s, got %s", env.Type, loaded.Type)
	}
	if loaded.Status != env.Status {
		t.Errorf("expected status %s, got %s", env.Status, loaded.Status)
	}
	if loaded.Config.Workers != env.Config.Workers {
		t.Errorf("expected workers %d, got %d", env.Config.Workers, loaded.Config.Workers)
	}
}

func TestLoadStateNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	_, err = mgr.loadState("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent environment")
	}
}

func TestExistsProduction(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))

	if !mgr.Exists("production") {
		t.Error("production should always exist")
	}
}

func TestExistsNonexistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	if mgr.Exists("nonexistent") {
		t.Error("nonexistent environment should not exist")
	}
}

func TestGetProduction(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))

	env, err := mgr.Get("production")
	if err != nil {
		t.Fatalf("get production failed: %v", err)
	}

	if env.Name != "production" {
		t.Errorf("expected name production, got %s", env.Name)
	}
	if env.Status != StatusRunning {
		t.Errorf("expected status running, got %s", env.Status)
	}
}

func TestGenerateKindConfig(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))

	env := &Environment{
		Name: "test",
		Config: EnvConfig{
			KindClusterName: "lab-test",
			Workers:         0,
		},
	}

	config := mgr.generateKindConfig(env)

	if config == "" {
		t.Error("expected non-empty config")
	}

	// Check for control-plane node
	if !contains(config, "role: control-plane") {
		t.Error("expected control-plane role in config")
	}

	// Check for cluster name
	if !contains(config, "name: lab-test") {
		t.Error("expected cluster name in config")
	}
}

func TestGenerateKindConfigWithWorkers(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))

	env := &Environment{
		Name: "test",
		Config: EnvConfig{
			KindClusterName: "lab-test",
			Workers:         2,
		},
	}

	config := mgr.generateKindConfig(env)

	// Count worker nodes
	workerCount := countOccurrences(config, "role: worker")
	if workerCount != 2 {
		t.Errorf("expected 2 worker roles, got %d", workerCount)
	}
}

func TestListIncludesProduction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	envs, err := mgr.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	found := false
	for _, e := range envs {
		if e.Name == "production" {
			found = true
			break
		}
	}

	if !found {
		t.Error("production should be in list")
	}
}

func TestListWithSavedEnvironment(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	// Create a test environment state
	env := &Environment{
		Name:   "staging",
		Type:   TypeKind,
		Status: StatusStopped,
		Config: EnvConfig{
			KindClusterName: "lab-staging",
		},
	}
	if err := mgr.saveState(env); err != nil {
		t.Fatalf("save state failed: %v", err)
	}

	envs, err := mgr.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	if len(envs) != 2 {
		t.Errorf("expected 2 environments (production + staging), got %d", len(envs))
	}

	names := make(map[string]bool)
	for _, e := range envs {
		names[e.Name] = true
	}

	if !names["production"] {
		t.Error("production should be in list")
	}
	if !names["staging"] {
		t.Error("staging should be in list")
	}
}

func TestCreateReservedName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	_, err = mgr.Create("production", "", 0)
	if err == nil {
		t.Error("expected error when creating production environment")
	}
}

func TestDeleteState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "env-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := NewManager(WithStateDir(tmpDir), WithConfigDir(tmpDir))

	// Create a test environment state
	env := &Environment{
		Name:   "test",
		Type:   TypeKind,
		Status: StatusStopped,
		Config: EnvConfig{
			KindClusterName: "lab-test",
		},
	}
	if err := mgr.saveState(env); err != nil {
		t.Fatalf("save state failed: %v", err)
	}

	// Verify it exists
	if !mgr.Exists("test") {
		t.Fatal("test environment should exist")
	}

	// Delete it
	if err := mgr.Delete("test"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	// Verify it's gone
	if mgr.Exists("test") {
		t.Error("test environment should not exist after delete")
	}

	// Verify state directory is removed
	stateDir := filepath.Dir(mgr.getStatePath("test"))
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Error("state directory should be removed")
	}
}

func TestGetKubeconfigProduction(t *testing.T) {
	mgr := NewManager(WithStateDir("/state"), WithConfigDir("/config"))

	_, err := mgr.GetKubeconfig("production")
	if err == nil {
		t.Error("expected error when getting production kubeconfig")
	}
}

// Helper functions
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countOccurrences(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}
