package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDir(t *testing.T) {
	dir := ConfigDir("env")
	if !strings.Contains(dir, "lab") {
		t.Errorf("expected path to contain 'lab', got %s", dir)
	}
	if !strings.HasSuffix(dir, filepath.Join("lab", "env")) {
		t.Errorf("expected path to end with lab/env, got %s", dir)
	}
}

func TestCacheDir(t *testing.T) {
	dir := CacheDir("k8s")
	if !strings.Contains(dir, "lab") {
		t.Errorf("expected path to contain 'lab', got %s", dir)
	}
	if !strings.HasSuffix(dir, filepath.Join("lab", "k8s")) {
		t.Errorf("expected path to end with lab/k8s, got %s", dir)
	}
}

func TestStateDir(t *testing.T) {
	dir := StateDir("env")
	if !strings.Contains(dir, "lab") {
		t.Errorf("expected path to contain 'lab', got %s", dir)
	}
	if !strings.HasSuffix(dir, filepath.Join("lab", "env")) {
		t.Errorf("expected path to end with lab/env, got %s", dir)
	}
}

func TestDataDir(t *testing.T) {
	dir := DataDir("test")
	if !strings.Contains(dir, "lab") {
		t.Errorf("expected path to contain 'lab', got %s", dir)
	}
	if !strings.HasSuffix(dir, filepath.Join("lab", "test")) {
		t.Errorf("expected path to end with lab/test, got %s", dir)
	}
}

func TestRuntimeDir(t *testing.T) {
	// RuntimeDir might be empty if XDG_RUNTIME_DIR is not set
	dir := RuntimeDir("env")
	if dir != "" {
		if !strings.Contains(dir, "lab") {
			t.Errorf("expected path to contain 'lab', got %s", dir)
		}
		if !strings.HasSuffix(dir, filepath.Join("lab", "env")) {
			t.Errorf("expected path to end with lab/env, got %s", dir)
		}
	}
}

func TestProjectConfigDir(t *testing.T) {
	dir := ProjectConfigDir()
	if dir != "config" {
		t.Errorf("expected 'config', got %s", dir)
	}
}

func TestXDGCompliance(t *testing.T) {
	// Test that we get valid XDG-compliant paths
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	// ConfigDir should contain 'lab' and be under home
	configDir := ConfigDir("test")
	if !strings.Contains(configDir, "lab") {
		t.Errorf("expected config dir to contain 'lab', got %s", configDir)
	}
	if !strings.HasPrefix(configDir, homeDir) {
		t.Errorf("expected config dir to be under home directory, got %s", configDir)
	}
	if !strings.HasSuffix(configDir, filepath.Join("lab", "test")) {
		t.Errorf("expected config dir to end with lab/test, got %s", configDir)
	}

	// CacheDir should contain 'lab' and be under home
	cacheDir := CacheDir("test")
	if !strings.Contains(cacheDir, "lab") {
		t.Errorf("expected cache dir to contain 'lab', got %s", cacheDir)
	}
	if !strings.HasPrefix(cacheDir, homeDir) {
		t.Errorf("expected cache dir to be under home directory, got %s", cacheDir)
	}
	if !strings.HasSuffix(cacheDir, filepath.Join("lab", "test")) {
		t.Errorf("expected cache dir to end with lab/test, got %s", cacheDir)
	}

	// StateDir should contain 'lab' and be under home
	stateDir := StateDir("test")
	if !strings.Contains(stateDir, "lab") {
		t.Errorf("expected state dir to contain 'lab', got %s", stateDir)
	}
	if !strings.HasPrefix(stateDir, homeDir) {
		t.Errorf("expected state dir to be under home directory, got %s", stateDir)
	}
	if !strings.HasSuffix(stateDir, filepath.Join("lab", "test")) {
		t.Errorf("expected state dir to end with lab/test, got %s", stateDir)
	}
}
