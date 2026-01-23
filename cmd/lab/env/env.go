// Package env handles Kind-based environment management for staging and ephemeral environments
package env

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
)

// EnvironmentType represents the type of environment
type EnvironmentType string

const (
	TypeKind EnvironmentType = "kind"
)

// EnvironmentStatus represents the current state of an environment
type EnvironmentStatus string

const (
	StatusStopped  EnvironmentStatus = "stopped"
	StatusRunning  EnvironmentStatus = "running"
	StatusCreating EnvironmentStatus = "creating"
	StatusError    EnvironmentStatus = "error"
)

// Environment represents a managed environment
type Environment struct {
	Name      string            `json:"name"`
	Type      EnvironmentType   `json:"type"`
	Status    EnvironmentStatus `json:"status"`
	FromEnv   string            `json:"from_env,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	TTL       time.Duration     `json:"ttl,omitempty"`
	Config    EnvConfig         `json:"config"`
}

// EnvConfig holds environment-specific configuration
type EnvConfig struct {
	KindClusterName string `json:"kind_cluster_name,omitempty"`
	Kubeconfig      string `json:"kubeconfig,omitempty"`
	Workers         int    `json:"workers,omitempty"`
}

// Manager handles environment operations
type Manager struct {
	stateDir  string
	configDir string
}

// ManagerOption is a functional option for configuring Manager
type ManagerOption func(*Manager)

// WithStateDir sets a custom state directory
func WithStateDir(dir string) ManagerOption {
	return func(m *Manager) {
		m.stateDir = dir
	}
}

// WithConfigDir sets a custom config directory
func WithConfigDir(dir string) ManagerOption {
	return func(m *Manager) {
		m.configDir = dir
	}
}

// NewManager creates a new environment manager with XDG-compliant defaults
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		stateDir:  paths.StateDir("env"),
		configDir: paths.ConfigDir("env"),
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// getStatePath returns the path to the state file for an environment
func (m *Manager) getStatePath(name string) string {
	return filepath.Join(m.stateDir, name, "state.json")
}

// getKubeconfigPath returns the path to the kubeconfig for an environment
func (m *Manager) getKubeconfigPath(name string) string {
	return filepath.Join(m.stateDir, name, "kubeconfig")
}

// getKindConfigPath returns the path to the Kind config for an environment
func (m *Manager) getKindConfigPath(name string) string {
	return filepath.Join(m.stateDir, name, "kind-config.yaml")
}

// saveState persists environment state to disk
func (m *Manager) saveState(env *Environment) error {
	statePath := m.getStatePath(env.Name)
	stateDir := filepath.Dir(statePath)

	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(statePath, data, 0600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	return nil
}

// loadState loads environment state from disk
func (m *Manager) loadState(name string) (*Environment, error) {
	statePath := m.getStatePath(name)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("environment %q not found", name)
		}
		return nil, fmt.Errorf("read state: %w", err)
	}

	var env Environment
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	return &env, nil
}

// Create creates a new Kind-based environment
func (m *Manager) Create(name, fromEnv string, workers int) (*Environment, error) {
	// Check if environment already exists
	if _, err := m.loadState(name); err == nil {
		return nil, fmt.Errorf("environment %q already exists", name)
	}

	// Validate that it's not a reserved name
	if name == "production" {
		return nil, fmt.Errorf("cannot create environment with reserved name %q", name)
	}

	// Create environment state
	env := &Environment{
		Name:      name,
		Type:      TypeKind,
		Status:    StatusCreating,
		FromEnv:   fromEnv,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config: EnvConfig{
			KindClusterName: fmt.Sprintf("lab-%s", name),
			Kubeconfig:      m.getKubeconfigPath(name),
			Workers:         workers,
		},
	}

	// Save initial state
	if err := m.saveState(env); err != nil {
		return nil, err
	}

	// Generate Kind configuration
	kindConfig := m.generateKindConfig(env)
	kindConfigPath := m.getKindConfigPath(name)
	if err := os.WriteFile(kindConfigPath, []byte(kindConfig), 0600); err != nil {
		env.Status = StatusError
		m.saveState(env)
		return nil, fmt.Errorf("write kind config: %w", err)
	}

	// Create the Kind cluster
	cmd := exec.Command("kind", "create", "cluster",
		"--name", env.Config.KindClusterName,
		"--config", kindConfigPath,
		"--kubeconfig", env.Config.Kubeconfig,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		env.Status = StatusError
		m.saveState(env)
		return nil, fmt.Errorf("create kind cluster: %w", err)
	}

	env.Status = StatusRunning
	env.UpdatedAt = time.Now()
	if err := m.saveState(env); err != nil {
		return nil, err
	}

	return env, nil
}

// generateKindConfig generates a Kind cluster configuration
func (m *Manager) generateKindConfig(env *Environment) string {
	workers := env.Config.Workers
	if workers < 0 {
		workers = 0
	}

	config := `# Kind cluster configuration for %s
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: %s
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
`

	result := fmt.Sprintf(config, env.Name, env.Config.KindClusterName)

	// Add worker nodes
	for i := 0; i < workers; i++ {
		result += "- role: worker\n"
	}

	return result
}

// Start starts a stopped environment
func (m *Manager) Start(name string) error {
	env, err := m.loadState(name)
	if err != nil {
		return err
	}

	if env.Status == StatusRunning {
		return fmt.Errorf("environment %q is already running", name)
	}

	// Check if Kind cluster exists but is stopped
	// Kind doesn't really support pause/resume, so we check if cluster exists
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("get kind clusters: %w", err)
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	found := false
	for _, c := range clusters {
		if c == env.Config.KindClusterName {
			found = true
			break
		}
	}

	if !found {
		// Cluster doesn't exist, need to recreate
		kindConfigPath := m.getKindConfigPath(name)
		cmd := exec.Command("kind", "create", "cluster",
			"--name", env.Config.KindClusterName,
			"--config", kindConfigPath,
			"--kubeconfig", env.Config.Kubeconfig,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("recreate kind cluster: %w", err)
		}
	}

	env.Status = StatusRunning
	env.UpdatedAt = time.Now()
	return m.saveState(env)
}

// Stop stops a running environment
func (m *Manager) Stop(name string, preserveState bool) error {
	env, err := m.loadState(name)
	if err != nil {
		return err
	}

	if env.Status != StatusRunning {
		return fmt.Errorf("environment %q is not running", name)
	}

	if !preserveState {
		// Delete the Kind cluster but keep state
		cmd := exec.Command("kind", "delete", "cluster",
			"--name", env.Config.KindClusterName,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("delete kind cluster: %w", err)
		}
	}

	env.Status = StatusStopped
	env.UpdatedAt = time.Now()
	return m.saveState(env)
}

// Delete permanently deletes an environment
func (m *Manager) Delete(name string) error {
	env, err := m.loadState(name)
	if err != nil {
		return err
	}

	// Delete the Kind cluster if it exists
	cmd := exec.Command("kind", "delete", "cluster",
		"--name", env.Config.KindClusterName,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Ignore errors - cluster might not exist
	cmd.Run()

	// Remove state directory
	stateDir := filepath.Dir(m.getStatePath(name))
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("remove state directory: %w", err)
	}

	return nil
}

// List returns all managed environments
func (m *Manager) List() ([]*Environment, error) {
	// Always include production as a "virtual" environment
	envs := []*Environment{
		{
			Name:   "production",
			Type:   "physical",
			Status: StatusRunning,
		},
	}

	// Check if state directory exists
	if _, err := os.Stat(m.stateDir); os.IsNotExist(err) {
		return envs, nil
	}

	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		return nil, fmt.Errorf("read state directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		env, err := m.loadState(entry.Name())
		if err != nil {
			continue // Skip invalid state files
		}

		// Update status by checking if Kind cluster is actually running
		if env.Type == TypeKind {
			env.Status = m.getKindClusterStatus(env.Config.KindClusterName)
		}

		envs = append(envs, env)
	}

	return envs, nil
}

// getKindClusterStatus checks if a Kind cluster is running
func (m *Manager) getKindClusterStatus(clusterName string) EnvironmentStatus {
	cmd := exec.Command("kind", "get", "clusters")
	output, err := cmd.Output()
	if err != nil {
		return StatusError
	}

	clusters := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, c := range clusters {
		if c == clusterName {
			return StatusRunning
		}
	}

	return StatusStopped
}

// Get returns a specific environment
func (m *Manager) Get(name string) (*Environment, error) {
	if name == "production" {
		return &Environment{
			Name:   "production",
			Type:   "physical",
			Status: StatusRunning,
		}, nil
	}

	env, err := m.loadState(name)
	if err != nil {
		return nil, err
	}

	// Update status
	if env.Type == TypeKind {
		env.Status = m.getKindClusterStatus(env.Config.KindClusterName)
	}

	return env, nil
}

// Exists checks if an environment exists
func (m *Manager) Exists(name string) bool {
	if name == "production" {
		return true
	}
	_, err := m.loadState(name)
	return err == nil
}

// GetKubeconfig returns the kubeconfig path for an environment
func (m *Manager) GetKubeconfig(name string) (string, error) {
	if name == "production" {
		return "", fmt.Errorf("use 'lab k8s kubeconfig decrypt production' for production kubeconfig")
	}

	env, err := m.loadState(name)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(env.Config.Kubeconfig); os.IsNotExist(err) {
		return "", fmt.Errorf("kubeconfig not found - environment may need to be started")
	}

	return env.Config.Kubeconfig, nil
}
