// Package paths provides XDG-compliant directory paths for lab subcommands
package paths

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

const appName = "lab"

// ConfigDir returns the XDG config directory for a subcommand
// Example: ~/.config/lab/env
func ConfigDir(subcommand string) string {
	return filepath.Join(xdg.ConfigHome, appName, subcommand)
}

// CacheDir returns the XDG cache directory for a subcommand
// Example: ~/.cache/lab/k8s
func CacheDir(subcommand string) string {
	return filepath.Join(xdg.CacheHome, appName, subcommand)
}

// StateDir returns the XDG state directory for a subcommand
// Example: ~/.local/state/lab/env
func StateDir(subcommand string) string {
	return filepath.Join(xdg.StateHome, appName, subcommand)
}

// DataDir returns the XDG data directory for a subcommand
// Example: ~/.local/share/lab/k8s
func DataDir(subcommand string) string {
	return filepath.Join(xdg.DataHome, appName, subcommand)
}

// RuntimeDir returns the XDG runtime directory for a subcommand
// Example: /run/user/1000/lab/env
// Returns empty string if XDG_RUNTIME_DIR is not set
func RuntimeDir(subcommand string) string {
	if xdg.RuntimeDir == "" {
		return ""
	}
	return filepath.Join(xdg.RuntimeDir, appName, subcommand)
}

// ProjectConfigDir returns the project-level config directory
// This is for reading CUE configuration files from the project
// Falls back to current working directory + /config
func ProjectConfigDir() string {
	// For project config, we want to use the current directory structure
	// not XDG, since these are source-controlled files
	// This will be overridden by LAB_CONFIG_DIR env var if set
	return "config"
}

// RepoRoot returns the git repository root directory
// by running `git rev-parse --show-toplevel`
// Returns an error if not in a git repository or git command fails
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", errors.New("no .git file or directory found in any parent")
}
