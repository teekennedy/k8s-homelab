// Package cmd provides the CLI interface for lab
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
)

var (
	// Flags
	verbose    bool
	jsonOutput bool
)

var rootCmd = &cobra.Command{
	Use:   "lab",
	Short: "Unified CLI for k8s-homelab management",
	Long: `lab is a unified command-line tool for managing your k8s-homelab infrastructure.

It provides commands for:
  - Environment management (create, start, stop staging/ephemeral environments)
  - NixOS host management (build, deploy, diff, bootstrap)
  - Kubernetes operations (bootstrap, diff, sync)
  - Terraform operations (plan, apply)
  - Configuration management (show, validate, export)`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
}

// getConfigDir returns the path to the project CUE configuration directory
// This is for reading CUE configuration files from the project (source-controlled)
func getConfigDir() string {
	if dir := os.Getenv("LAB_CONFIG_DIR"); dir != "" {
		return dir
	}
	// Default to config/ relative to current working directory
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not get working directory: %v\n", err)
		return paths.ProjectConfigDir()
	}
	return filepath.Join(cwd, paths.ProjectConfigDir())
}

// printJSON prints data as JSON to stdout
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
