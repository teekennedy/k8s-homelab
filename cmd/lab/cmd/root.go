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
	verbose    bool
	jsonOutput bool
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
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

	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	cmd.AddCommand(newCICmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newEnvCmd())
	cmd.AddCommand(newK8sCmd())
	cmd.AddCommand(newHostCmd())
	cmd.AddCommand(newTFCmd())

	return cmd
}

func Execute() error {
	return newRootCmd().Execute()
}

func getConfigDir() string {
	if dir := os.Getenv("LAB_CONFIG_DIR"); dir != "" {
		return dir
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not get working directory: %v\n", err)
		return paths.ProjectConfigDir()
	}
	return filepath.Join(cwd, paths.ProjectConfigDir())
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
