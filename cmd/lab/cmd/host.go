package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var hostCmd = &cobra.Command{
	Use:   "host",
	Short: "Manage NixOS hosts",
	Long:  `Commands for building, deploying, and managing NixOS hosts.`,
}

var hostBuildCmd = &cobra.Command{
	Use:   "build <hostname>",
	Short: "Build NixOS configuration for a host",
	Long:  `Build the NixOS configuration for the specified host without deploying.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		fmt.Printf("Building NixOS configuration for %s...\n", hostname)

		// Run nix build
		nixCmd := exec.Command("nix", "build",
			fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
			"--no-link", "--print-out-paths")
		nixCmd.Stdout = os.Stdout
		nixCmd.Stderr = os.Stderr
		return nixCmd.Run()
	},
}

var hostDeployCmd = &cobra.Command{
	Use:   "deploy <hostname>",
	Short: "Deploy NixOS configuration to a host",
	Long:  `Deploy the NixOS configuration to the specified host using deploy-rs.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		fmt.Printf("Deploying to %s...\n", hostname)

		deployArgs := []string{".", "--", "--targets", fmt.Sprintf(".#%s", hostname)}
		if dryRun {
			deployArgs = append(deployArgs, "--dry-activate")
		}

		deployCmd := exec.Command("deploy", deployArgs...)
		deployCmd.Stdout = os.Stdout
		deployCmd.Stderr = os.Stderr
		return deployCmd.Run()
	},
}

var hostDiffCmd = &cobra.Command{
	Use:   "diff <hostname>",
	Short: "Show pending changes for a host",
	Long:  `Show what would change if the host configuration was deployed.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		fmt.Printf("Showing diff for %s...\n", hostname)
		fmt.Println("Note: Full diff implementation will be added in Phase 2")

		// For now, just show what would be built
		nixCmd := exec.Command("nix", "build",
			fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
			"--no-link", "--print-out-paths", "--dry-run")
		nixCmd.Stdout = os.Stdout
		nixCmd.Stderr = os.Stderr
		return nixCmd.Run()
	},
}

var hostListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured hosts",
	Long:  `List all hosts configured in the environment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// This will eventually read from CUE config
		fmt.Println("Configured hosts:")
		fmt.Println("  borg-0 (10.69.80.10) - server, clusterInit")
		fmt.Println("  borg-2 (10.69.80.12) - server")
		fmt.Println("  borg-3 (10.69.80.13) - server")
		return nil
	},
}

var hostBootstrapCmd = &cobra.Command{
	Use:   "bootstrap <hostname>",
	Short: "Bootstrap a new host",
	Long:  `Bootstrap a new host from scratch using nixos-anywhere.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		ip, _ := cmd.Flags().GetString("ip")
		fmt.Printf("Bootstrapping new host %s at %s\n", hostname, ip)
		fmt.Println("Note: Bootstrap implementation will be added in Phase 2")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(hostCmd)

	hostCmd.AddCommand(hostBuildCmd)

	hostDeployCmd.Flags().Bool("dry-run", false, "Perform a dry run without making changes")
	hostCmd.AddCommand(hostDeployCmd)

	hostCmd.AddCommand(hostDiffCmd)
	hostCmd.AddCommand(hostListCmd)

	hostBootstrapCmd.Flags().String("ip", "", "IP address of the new host")
	hostBootstrapCmd.MarkFlagRequired("ip")
	hostCmd.AddCommand(hostBootstrapCmd)
}
