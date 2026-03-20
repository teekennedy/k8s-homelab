package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI/CD operations",
		Long: `Commands for running CI/CD pipelines via Dagger.

These commands use the devenv containers.ci environment, which includes all
CI dependencies but does NOT include lab itself. This breaks the circular
dependency and allows the CI pipeline to build lab from scratch.`,
	}

	cmd.AddCommand(newCIBuildCmd())
	cmd.AddCommand(newCILintCmd())
	cmd.AddCommand(newCITestCmd())
	cmd.AddCommand(newCIValidateCmd())
	cmd.AddCommand(newCIAllCmd())

	return cmd
}

func newCIBuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build [components...]",
		Short: "Build components via Dagger",
		Long: `Build one or more components using the Dagger CI pipeline.

If no components are specified, builds everything.

Examples:
  lab ci build              # Build all components
  lab ci build lab          # Build just the lab CLI
  lab ci build k8s          # Render all Helm templates
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

	cmd.Flags().Bool("changed", false, "Only build changed components")
	cmd.AddCommand(newCIBuildK8sCmd())

	return cmd
}

func newCILintCmd() *cobra.Command {
	cmd := &cobra.Command{
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
				daggerArgs = append(daggerArgs, "export", "--path=.")
				fmt.Println("Running linters with auto-fix enabled...")
			}
			return runDaggerInCI(daggerArgs...)
		},
	}

	cmd.Flags().Bool("fix", false, "Automatically fix issues where possible")

	return cmd
}

func newCITestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Run tests via Dagger",
		Long:  `Run all tests via Dagger.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return runDaggerInCI("call", "test", "--source=.")
		},
	}
}

func newCIValidateCmd() *cobra.Command {
	return &cobra.Command{
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
}

func newCIAllCmd() *cobra.Command {
	cmd := &cobra.Command{
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
				daggerArgs = append(daggerArgs, "export", "--path=.")
				fmt.Println("Running CI pipeline with auto-fix enabled...")
			}
			return runDaggerInCI(daggerArgs...)
		},
	}

	cmd.Flags().Bool("fix", false, "Automatically fix linting issues")

	return cmd
}

func runDaggerInCI(args ...string) error {
	if os.Getenv("HOMELAB_CI_CONTAINER") == "true" {
		return runDagger(args...)
	}
	return runDagger(args...)
}

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
