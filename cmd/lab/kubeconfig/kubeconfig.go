// Package kubeconfig handles environment-aware kubeconfig management with sops decryption
package kubeconfig

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// Manager handles kubeconfig files for different environments
type Manager struct {
	configDir   string
	cacheDir    string
	mu          sync.Mutex
	activeEnv   string
	tempFile    string
	originalEnv string
}

// NewManager creates a new kubeconfig manager
func NewManager(configDir, cacheDir string) *Manager {
	return &Manager{
		configDir: configDir,
		cacheDir:  cacheDir,
	}
}

// GetEncryptedPath returns the path to the encrypted kubeconfig for an environment
func (m *Manager) GetEncryptedPath(env string) string {
	return filepath.Join(m.configDir, "kubeconfig", env+".enc.yaml")
}

// GetDecryptedPath returns the path where the decrypted kubeconfig will be stored
func (m *Manager) GetDecryptedPath(env string) string {
	return filepath.Join(m.cacheDir, "kubeconfig", env+".yaml")
}

// Exists checks if a kubeconfig exists for the given environment
func (m *Manager) Exists(env string) bool {
	_, err := os.Stat(m.GetEncryptedPath(env))
	return err == nil
}

// Decrypt decrypts the kubeconfig for the given environment and returns the content
func (m *Manager) Decrypt(env string) ([]byte, error) {
	encPath := m.GetEncryptedPath(env)

	if _, err := os.Stat(encPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("kubeconfig for environment %q not found at %s", env, encPath)
	}

	// Use sops to decrypt
	cmd := exec.Command("sops", "decrypt", encPath)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("sops decrypt failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("sops decrypt failed: %w", err)
	}

	return output, nil
}

// Setup decrypts the kubeconfig and sets up the environment for kubectl/helm commands
// It writes the decrypted kubeconfig to a temp file and sets KUBECONFIG env var
// Returns a cleanup function that should be called when done
func (m *Manager) Setup(env string) (cleanup func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store original KUBECONFIG
	m.originalEnv = os.Getenv("KUBECONFIG")
	m.activeEnv = env

	// Decrypt the kubeconfig
	content, err := m.Decrypt(env)
	if err != nil {
		return nil, err
	}

	// Create the cache directory if it doesn't exist
	kubeconfigCacheDir := filepath.Join(m.cacheDir, "kubeconfig")
	if err := os.MkdirAll(kubeconfigCacheDir, 0700); err != nil {
		return nil, fmt.Errorf("create kubeconfig cache dir: %w", err)
	}

	// Write to temp file with restricted permissions
	tempPath := m.GetDecryptedPath(env)
	if err := os.WriteFile(tempPath, content, 0600); err != nil {
		return nil, fmt.Errorf("write decrypted kubeconfig: %w", err)
	}
	m.tempFile = tempPath

	// Set KUBECONFIG environment variable
	os.Setenv("KUBECONFIG", tempPath)

	// Return cleanup function
	cleanup = func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		// Remove the temp file
		if m.tempFile != "" {
			os.Remove(m.tempFile)
			m.tempFile = ""
		}

		// Restore original KUBECONFIG
		if m.originalEnv != "" {
			os.Setenv("KUBECONFIG", m.originalEnv)
		} else {
			os.Unsetenv("KUBECONFIG")
		}
		m.activeEnv = ""
	}

	return cleanup, nil
}

// SetupPersistent sets up the kubeconfig without automatic cleanup
// The decrypted file will persist until explicitly cleaned up
func (m *Manager) SetupPersistent(env string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Decrypt the kubeconfig
	content, err := m.Decrypt(env)
	if err != nil {
		return err
	}

	// Create the cache directory if it doesn't exist
	kubeconfigCacheDir := filepath.Join(m.cacheDir, "kubeconfig")
	if err := os.MkdirAll(kubeconfigCacheDir, 0700); err != nil {
		return fmt.Errorf("create kubeconfig cache dir: %w", err)
	}

	// Write to file with restricted permissions
	decPath := m.GetDecryptedPath(env)
	if err := os.WriteFile(decPath, content, 0600); err != nil {
		return fmt.Errorf("write decrypted kubeconfig: %w", err)
	}

	// Set KUBECONFIG environment variable
	os.Setenv("KUBECONFIG", decPath)
	m.activeEnv = env
	m.tempFile = decPath

	return nil
}

// ActiveEnvironment returns the currently active environment
func (m *Manager) ActiveEnvironment() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeEnv
}

// Cleanup removes any decrypted kubeconfig files and restores the environment
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tempFile != "" {
		os.Remove(m.tempFile)
		m.tempFile = ""
	}

	if m.originalEnv != "" {
		os.Setenv("KUBECONFIG", m.originalEnv)
	} else {
		os.Unsetenv("KUBECONFIG")
	}
	m.activeEnv = ""
}

// CleanupAll removes all decrypted kubeconfig files from the cache directory
func (m *Manager) CleanupAll() error {
	kubeconfigCacheDir := filepath.Join(m.cacheDir, "kubeconfig")
	if _, err := os.Stat(kubeconfigCacheDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(kubeconfigCacheDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			os.Remove(filepath.Join(kubeconfigCacheDir, entry.Name()))
		}
	}

	return nil
}

// ListEnvironments returns a list of environments that have kubeconfig files
func (m *Manager) ListEnvironments() ([]string, error) {
	kubeconfigDir := filepath.Join(m.configDir, "kubeconfig")
	if _, err := os.Stat(kubeconfigDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(kubeconfigDir)
	if err != nil {
		return nil, err
	}

	var envs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			name := entry.Name()
			if filepath.Ext(name) == ".yaml" && len(name) > 9 { // .enc.yaml = 9 chars
				if name[len(name)-9:] == ".enc.yaml" {
					envs = append(envs, name[:len(name)-9])
				}
			}
		}
	}

	return envs, nil
}

// WithKubeconfig executes a function with the kubeconfig for the given environment
// The kubeconfig is automatically set up and cleaned up
func (m *Manager) WithKubeconfig(env string, fn func() error) error {
	cleanup, err := m.Setup(env)
	if err != nil {
		return err
	}
	defer cleanup()

	return fn()
}

// GetKubeconfigEnv returns the KUBECONFIG path for the given environment
// without modifying the current environment
func (m *Manager) GetKubeconfigEnv(env string) (string, error) {
	// Check if already decrypted
	decPath := m.GetDecryptedPath(env)
	if _, err := os.Stat(decPath); err == nil {
		return decPath, nil
	}

	// Need to decrypt first
	content, err := m.Decrypt(env)
	if err != nil {
		return "", err
	}

	// Create the cache directory if it doesn't exist
	kubeconfigCacheDir := filepath.Join(m.cacheDir, "kubeconfig")
	if err := os.MkdirAll(kubeconfigCacheDir, 0700); err != nil {
		return "", fmt.Errorf("create kubeconfig cache dir: %w", err)
	}

	// Write to file
	if err := os.WriteFile(decPath, content, 0600); err != nil {
		return "", fmt.Errorf("write decrypted kubeconfig: %w", err)
	}

	return decPath, nil
}
