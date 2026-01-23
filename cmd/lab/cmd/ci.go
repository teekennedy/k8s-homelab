package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var ciCmd = &cobra.Command{
	Use:   "ci",
	Short: "CI/CD operations",
	Long: `Commands for running CI/CD pipelines via Dagger.

These commands use the devenv containers.ci environment, which includes all
CI dependencies but does NOT include lab itself. This breaks the circular
dependency and allows the CI pipeline to build lab from scratch.`,
}

var ciBuildCmd = &cobra.Command{
	Use:   "build [components...]",
	Short: "Build components via Dagger",
	Long: `Build one or more components using the Dagger CI pipeline.

If no components are specified, builds everything.

Examples:
  lab ci build              # Build all components
  lab ci build lab          # Build just the lab CLI
  lab ci build nix          # Build NixOS configurations
  lab ci build --changed    # Build only changed components`,
	RunE: func(cmd *cobra.Command, args []string) error {
		changed, _ := cmd.Flags().GetBool("changed")
		cmd.SilenceUsage = true

		daggerArgs := []string{"call", "build"}
		if changed {
			daggerArgs = append(daggerArgs, "--changed-only")
		}
		if len(args) > 0 {
			daggerArgs = append(daggerArgs, "--components", args[0])
		}

		return runDaggerInCI(daggerArgs...)
	},
}

var ciLintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Run linters via Dagger",
	Long: `Run all linters (deadnix, alejandra, yamllint, go vet, etc.) via Dagger.

Use --fix to automatically fix issues where possible (Nix formatting, Go formatting).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fix, _ := cmd.Flags().GetBool("fix")
		cmd.SilenceUsage = true
		daggerArgs := []string{"call", "lint", "--source=."}
		if fix {
			daggerArgs = append(daggerArgs, "--fix")
			// Export the fixed source back
			daggerArgs = append(daggerArgs, "export", "--path=.")
			fmt.Println("Running linters with auto-fix enabled...")
		}
		return runDaggerInCI(daggerArgs...)
	},
}

var ciTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Run tests via Dagger",
	Long:  `Run all tests via Dagger.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runDaggerInCI("call", "test", "--source=.")
	},
}

var ciValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate all configurations via Dagger",
	Long: `Validate all configurations (CUE, Nix, Terraform, etc.) via Dagger.

This runs:
  - nix flake check
  - lab config validate
  - tofu validate
  - helm lint`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runDaggerInCI("call", "validate", "--source=.")
	},
}

var ciAllCmd = &cobra.Command{
	Use:   "all",
	Short: "Run full CI pipeline (lint, validate, build, test)",
	Long: `Run the complete CI pipeline: lint → validate → build → test.

Use --fix to automatically fix linting issues during the pipeline.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fix, _ := cmd.Flags().GetBool("fix")
		cmd.SilenceUsage = true
		daggerArgs := []string{"call", "all", "--source=."}
		if fix {
			daggerArgs = append(daggerArgs, "--fix")
			// Export the fixed source back
			daggerArgs = append(daggerArgs, "export", "--path=.")
			fmt.Println("Running CI pipeline with auto-fix enabled...")
		}
		return runDaggerInCI(daggerArgs...)
	},
}

// runDaggerInCI runs dagger commands inside the CI container environment
// This avoids the circular dependency on lab being built
func runDaggerInCI(args ...string) error {
	// Check if we're already in the CI environment
	if os.Getenv("HOMELAB_CI_CONTAINER") == "true" {
		// We're in the container, just run dagger directly
		return runDagger(args...)
	}

	// We're outside the container, need to enter it
	// For now, just run dagger directly assuming devenv shell is active
	// TODO: Implement container entry when devenv containers support is stable
	return runDagger(args...)
}

// runDagger executes a dagger command
func runDagger(args ...string) error {
	cmd := exec.Command("dagger", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if verbose {
		fmt.Printf("Running: dagger %v\n", args)
	}

	return cmd.Run()
}

func init() {
	rootCmd.AddCommand(ciCmd)

	ciBuildCmd.Flags().Bool("changed", false, "Only build changed components")
	ciCmd.AddCommand(ciBuildCmd)

	ciLintCmd.Flags().Bool("fix", false, "Automatically fix issues where possible")
	ciCmd.AddCommand(ciLintCmd)

	ciCmd.AddCommand(ciTestCmd)
	ciCmd.AddCommand(ciValidateCmd)

	ciAllCmd.Flags().Bool("fix", false, "Automatically fix linting issues")
	ciCmd.AddCommand(ciAllCmd)
}
