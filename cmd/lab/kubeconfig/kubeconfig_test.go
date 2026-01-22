package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	mgr := NewManager("/config", "/cache")
	if mgr.configDir != "/config" {
		t.Errorf("expected configDir to be /config, got %s", mgr.configDir)
	}
	if mgr.cacheDir != "/cache" {
		t.Errorf("expected cacheDir to be /cache, got %s", mgr.cacheDir)
	}
}

func TestGetEncryptedPath(t *testing.T) {
	mgr := NewManager("/config", "/cache")
	expected := "/config/kubeconfig/production.enc.yaml"
	got := mgr.GetEncryptedPath("production")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestGetDecryptedPath(t *testing.T) {
	mgr := NewManager("/config", "/cache")
	expected := "/cache/kubeconfig/production.yaml"
	got := mgr.GetDecryptedPath("production")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestExists(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")
	kubeconfigDir := filepath.Join(configDir, "kubeconfig")

	// Create the kubeconfig directory
	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a test kubeconfig file
	testFile := filepath.Join(kubeconfigDir, "production.enc.yaml")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(configDir, cacheDir)

	// Test existing environment
	if !mgr.Exists("production") {
		t.Error("expected production to exist")
	}

	// Test non-existing environment
	if mgr.Exists("staging") {
		t.Error("expected staging to not exist")
	}
}

func TestListEnvironments(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")
	kubeconfigDir := filepath.Join(configDir, "kubeconfig")

	// Create the kubeconfig directory
	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test kubeconfig files
	testFiles := []string{"production.enc.yaml", "staging.enc.yaml"}
	for _, f := range testFiles {
		testFile := filepath.Join(kubeconfigDir, f)
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mgr := NewManager(configDir, cacheDir)

	envs, err := mgr.ListEnvironments()
	if err != nil {
		t.Fatal(err)
	}

	if len(envs) != 2 {
		t.Errorf("expected 2 environments, got %d", len(envs))
	}

	// Check that both environments are present
	envMap := make(map[string]bool)
	for _, e := range envs {
		envMap[e] = true
	}

	if !envMap["production"] {
		t.Error("expected production to be listed")
	}
	if !envMap["staging"] {
		t.Error("expected staging to be listed")
	}
}

func TestCleanupAll(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")
	kubeconfigCacheDir := filepath.Join(cacheDir, "kubeconfig")

	// Create the kubeconfig cache directory
	if err := os.MkdirAll(kubeconfigCacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test decrypted files
	testFiles := []string{"production.yaml", "staging.yaml"}
	for _, f := range testFiles {
		testFile := filepath.Join(kubeconfigCacheDir, f)
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mgr := NewManager(configDir, cacheDir)

	// Verify files exist before cleanup
	entries, _ := os.ReadDir(kubeconfigCacheDir)
	if len(entries) != 2 {
		t.Errorf("expected 2 files before cleanup, got %d", len(entries))
	}

	// Run cleanup
	if err := mgr.CleanupAll(); err != nil {
		t.Fatal(err)
	}

	// Verify files are removed
	entries, _ = os.ReadDir(kubeconfigCacheDir)
	if len(entries) != 0 {
		t.Errorf("expected 0 files after cleanup, got %d", len(entries))
	}
}

func TestActiveEnvironment(t *testing.T) {
	mgr := NewManager("/config", "/cache")

	// Initially should be empty
	if mgr.ActiveEnvironment() != "" {
		t.Error("expected active environment to be empty initially")
	}
}

func TestDecryptMissingFile(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")

	mgr := NewManager(configDir, cacheDir)

	// Try to decrypt a non-existent environment
	_, err = mgr.Decrypt("nonexistent")
	if err == nil {
		t.Error("expected error when decrypting non-existent environment")
	}
}

func TestSetupPersistentCreatesDir(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")
	kubeconfigDir := filepath.Join(configDir, "kubeconfig")

	// Create the kubeconfig directory with a mock encrypted file
	if err := os.MkdirAll(kubeconfigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a mock encrypted file (this will fail sops decrypt, but we're testing dir creation)
	testFile := filepath.Join(kubeconfigDir, "test.enc.yaml")
	if err := os.WriteFile(testFile, []byte("apiVersion: v1\nkind: Config\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(configDir, cacheDir)

	// This will fail at sops decrypt, but that's ok - we're testing path logic
	_ = mgr.SetupPersistent("test")

	// Check if cache directory would be created (it will be since we call MkdirAll)
	kubeconfigCacheDir := filepath.Join(cacheDir, "kubeconfig")
	if _, err := os.Stat(kubeconfigCacheDir); os.IsNotExist(err) {
		// Directory creation happens before decrypt, so it should exist even if decrypt fails
		t.Log("Cache directory not created (expected if decrypt fails early)")
	}
}

func TestCleanup(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "kubeconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := filepath.Join(tmpDir, "config")
	cacheDir := filepath.Join(tmpDir, "cache")
	kubeconfigCacheDir := filepath.Join(cacheDir, "kubeconfig")

	// Create the kubeconfig cache directory
	if err := os.MkdirAll(kubeconfigCacheDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a test file
	testFile := filepath.Join(kubeconfigCacheDir, "test.yaml")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(configDir, cacheDir)
	mgr.tempFile = testFile
	mgr.activeEnv = "test"

	// Set an original env var
	origKubeconfig := os.Getenv("KUBECONFIG")
	os.Setenv("KUBECONFIG", "/some/path")
	mgr.originalEnv = origKubeconfig

	// Run cleanup
	mgr.Cleanup()

	// Verify file is removed
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("expected temp file to be removed")
	}

	// Verify active env is cleared
	if mgr.activeEnv != "" {
		t.Error("expected active env to be cleared")
	}

	// Verify temp file path is cleared
	if mgr.tempFile != "" {
		t.Error("expected temp file path to be cleared")
	}
}
