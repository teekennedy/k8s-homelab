package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var tfCmd = &cobra.Command{
	Use:   "tf",
	Short: "Terraform/OpenTofu operations",
	Long:  `Commands for managing Terraform/OpenTofu infrastructure.`,
}

var tfPlanCmd = &cobra.Command{
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

var tfApplyCmd = &cobra.Command{
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

var tfListCmd = &cobra.Command{
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

func init() {
	rootCmd.AddCommand(tfCmd)

	tfPlanCmd.Flags().String("env", "production", "Target environment")
	tfCmd.AddCommand(tfPlanCmd)

	tfApplyCmd.Flags().String("env", "production", "Target environment")
	tfApplyCmd.Flags().Bool("auto-approve", false, "Skip interactive approval")
	tfCmd.AddCommand(tfApplyCmd)

	tfCmd.AddCommand(tfListCmd)
}
