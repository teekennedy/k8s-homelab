package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newTFCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tf",
		Short: "Terraform/OpenTofu operations",
		Long:  `Commands for managing Terraform/OpenTofu infrastructure.`,
	}

	cmd.AddCommand(newTFPlanCmd())
	cmd.AddCommand(newTFApplyCmd())
	cmd.AddCommand(newTFListCmd())

	return cmd
}

func newTFPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <module>",
		Short: "Plan Terraform changes",
		Long:  `Run terraform/tofu plan for the specified module.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			module := args[0]
			env, _ := cmd.Flags().GetString("env")

			fmt.Printf("Planning Terraform module %s (env=%s)...\n", module, env)

			tofuCmd := exec.Command("tofu", "plan")
			tofuCmd.Dir = fmt.Sprintf("terraform/%s", module)
			tofuCmd.Stdout = os.Stdout
			tofuCmd.Stderr = os.Stderr
			return tofuCmd.Run()
		},
	}

	cmd.Flags().String("env", "production", "Target environment")

	return cmd
}

func newTFApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <module>",
		Short: "Apply Terraform changes",
		Long:  `Run terraform/tofu apply for the specified module.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			module := args[0]
			env, _ := cmd.Flags().GetString("env")
			autoApprove, _ := cmd.Flags().GetBool("auto-approve")

			fmt.Printf("Applying Terraform module %s (env=%s)...\n", module, env)

			applyArgs := []string{"apply"}
			if autoApprove {
				applyArgs = append(applyArgs, "-auto-approve")
			}

			tofuCmd := exec.Command("tofu", applyArgs...)
			tofuCmd.Dir = fmt.Sprintf("terraform/%s", module)
			tofuCmd.Stdout = os.Stdout
			tofuCmd.Stderr = os.Stderr
			tofuCmd.Stdin = os.Stdin
			return tofuCmd.Run()
		},
	}

	cmd.Flags().String("env", "production", "Target environment")
	cmd.Flags().Bool("auto-approve", false, "Skip interactive approval")

	return cmd
}

func newTFListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Terraform modules",
		Long:  `List all available Terraform modules.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Terraform modules:")

			entries, err := os.ReadDir("terraform")
			if err != nil {
				return fmt.Errorf("read terraform directory: %w", err)
			}

			for _, entry := range entries {
				if entry.IsDir() && entry.Name() != ".terraform" {
					fmt.Printf("  - %s\n", entry.Name())
				}
			}
			return nil
		},
	}
}
