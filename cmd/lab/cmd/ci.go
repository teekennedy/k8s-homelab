package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci [check]",
		Short: "Run CI checks via Dagger",
		Long: `Run CI checks via Dagger.

Uses "dagger check" which runs checks in parallel. If a check name is
provided, it is globbed with a trailing * to match all checks starting
with that name (e.g. "lint" runs all lint* checks).

Lint checks that support formatting (lint-nix, lint-go, lint-python) return
changesets. Use --fix to automatically apply formatting fixes.

When --changed is passed, uses "dagger call" to pass the --paths parameter.
This requires a specific check name (e.g. "lint-nix", not "lint").

Examples:
  lab ci                    # Run all checks in parallel
  lab ci lint               # Run all lint* checks in parallel
  lab ci build              # Run all build* checks in parallel
  lab ci test               # Run all test* checks in parallel
  lab ci validate           # Run all validate* checks in parallel
  lab ci --fix              # Run all checks, auto-apply formatting fixes
  lab ci lint --fix         # Run lint checks, auto-apply formatting fixes
  lab ci test-go --changed  # Run test-go check on changed files only`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fix, _ := cmd.Flags().GetBool("fix")
			changed, _ := cmd.Flags().GetBool("changed")
			cmd.SilenceUsage = true

			// dagger check doesn't support function-level parameters like
			// --paths, so fall back to dagger call when --changed is used.
			if changed {
				if len(args) == 0 {
					return fmt.Errorf("a check name is required when using --changed")
				}

				daggerArgs := []string{"call", args[0], "--source=."}

				paths, err := changedFiles()
				if err != nil {
					return fmt.Errorf("detect changes: %w", err)
				}
				if len(paths) == 0 {
					fmt.Println("No changes detected")
					return nil
				}
				for _, p := range paths {
					daggerArgs = append(daggerArgs, "--paths", p)
				}
				if verbose {
					fmt.Printf("Changed files: %s\n", strings.Join(paths, ", "))
				}

				return runDagger(daggerArgs...)
			}

			// Lint checks return changesets with formatting fixes.
			// --fix maps to --auto-apply which applies those changesets.
			daggerArgs := []string{"check"}
			if fix {
				daggerArgs = append(daggerArgs, "--auto-apply")
			}
			if len(args) > 0 {
				daggerArgs = append(daggerArgs, args[0]+"*")
			}

			return runDagger(daggerArgs...)
		},
	}

	cmd.Flags().Bool("fix", false, "Automatically fix linting issues")
	cmd.Flags().Bool("changed", false, "Only run checks on changed files")

	return cmd
}

// changedFiles returns file paths changed in the working tree relative to HEAD.
func changedFiles() ([]string, error) {
	out, err := exec.Command("git", "diff", "--name-only", "HEAD").Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
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
