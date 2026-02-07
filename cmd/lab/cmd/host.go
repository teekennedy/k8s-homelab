package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
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
		showTrace, _ := cmd.Flags().GetBool("show-trace")

		// Validate host exists in config
		repoRoot, err := validateHost(hostname)
		if err != nil {
			return err
		}

		if !jsonOutput {
			fmt.Printf("Building NixOS configuration for %s...\n", hostname)
		}

		// Build nix command
		nixArgs := []string{
			"build",
			fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
			"--no-link",
			"--print-out-paths",
		}
		if showTrace {
			nixArgs = append(nixArgs, "--show-trace")
		}

		nixCmd := exec.Command("nix", nixArgs...)
		nixCmd.Stderr = os.Stderr
		nixCmd.Dir = repoRoot

		output, err := nixCmd.Output()
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}

		storePath := strings.TrimSpace(string(output))

		if jsonOutput {
			result := map[string]string{
				"host":      hostname,
				"storePath": storePath,
				"status":    "success",
			}
			out, _ := json.Marshal(result)
			fmt.Println(string(out))
		} else {
			fmt.Printf("Build successful: %s\n", storePath)
		}

		return nil
	},
}

var hostDeployCmd = &cobra.Command{
	Use:   "deploy <hostname>",
	Short: "Deploy NixOS configuration to a host",
	Long:  `Deploy the NixOS configuration to the specified host using deploy-rs.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		skipChecks, _ := cmd.Flags().GetBool("skip-checks")
		boot, _ := cmd.Flags().GetBool("boot")

		// Validate host exists
		repoRoot, err := validateHost(hostname)
		if err != nil {
			return err
		}

		if !jsonOutput {
			if dryRun {
				fmt.Printf("Dry-run deploy to %s...\n", hostname)
			} else {
				fmt.Printf("Deploying to %s...\n", hostname)
			}
		}
		cmd.SilenceUsage = true

		// Build deploy-rs command
		// deploy-rs expects: deploy [FLAGS] [FLAKE]
		var deployArgs []string
		if skipChecks {
			deployArgs = append(deployArgs, "--skip-checks")
		}
		deployArgs = append(deployArgs, "--targets", fmt.Sprintf(".#%s", hostname))
		if dryRun {
			deployArgs = append(deployArgs, "--dry-activate")
		}
		if boot {
			deployArgs = append(deployArgs, "--boot")
		}

		deployCmd := exec.Command("deploy", deployArgs...)
		deployCmd.Stdout = os.Stdout
		deployCmd.Stderr = os.Stderr
		deployCmd.Stdin = os.Stdin
		deployCmd.Dir = repoRoot

		if err := deployCmd.Run(); err != nil {
			return fmt.Errorf("deploy failed: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"host":   hostname,
				"dryRun": dryRun,
				"status": "success",
			}
			out, _ := json.Marshal(result)
			fmt.Println(string(out))
		}

		return nil
	},
}

var hostDiffCmd = &cobra.Command{
	Use:   "diff <hostname>",
	Short: "Show pending changes for a host",
	Long: `Show what would change if the host configuration was deployed.
Uses nvd (nix-visualize-derivation) to show a human-readable diff.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]

		// Validate host exists
		repoRoot, err := validateHost(hostname)
		if err != nil {
			return err
		}

		if !jsonOutput {
			fmt.Printf("Computing diff for %s...\n", hostname)
		}

		// First, build the new configuration
		nixBuildArgs := []string{
			"build",
			fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
			"--no-link",
			"--print-out-paths",
		}

		nixCmd := exec.Command("nix", nixBuildArgs...)
		nixCmd.Stderr = os.Stderr
		nixCmd.Dir = repoRoot
		newPathBytes, err := nixCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to build new configuration: %w", err)
		}
		newPath := strings.TrimSpace(string(newPathBytes))

		// Get the current system path from the remote host
		sshCmd := exec.Command("ssh", hostname, "readlink", "-f", "/run/current-system")
		currentPathBytes, err := sshCmd.Output()
		if err != nil {
			// If we can't reach the host, show what would be deployed
			if !jsonOutput {
				fmt.Printf("Cannot reach %s, showing build output:\n", hostname)
				fmt.Println(newPath)
			}
			return nil
		}
		currentPath := strings.TrimSpace(string(currentPathBytes))

		if currentPath == newPath {
			if !jsonOutput {
				fmt.Println("No changes - system is up to date")
			} else {
				result := map[string]interface{}{
					"host":    hostname,
					"changed": false,
				}
				out, _ := json.Marshal(result)
				fmt.Println(string(out))
			}
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Changes from %s to %s:\n\n", currentPath, newPath)
		}

		// Try to use nvd for pretty diff, fall back to nix store diff-closures
		nvdCmd := exec.Command("nvd", "diff", currentPath, newPath)
		nvdCmd.Stdout = os.Stdout
		nvdCmd.Stderr = os.Stderr
		if err := nvdCmd.Run(); err != nil {
			// Fallback to nix store diff-closures
			nixDiffCmd := exec.Command("nix", "store", "diff-closures", currentPath, newPath)
			nixDiffCmd.Stdout = os.Stdout
			nixDiffCmd.Stderr = os.Stderr
			return nixDiffCmd.Run()
		}

		return nil
	},
}

var hostListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured hosts",
	Long:  `List all hosts configured in the environment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")
		configDir := getConfigDir()

		env, err := config.LoadEnvironment(configDir, envName)
		if err != nil {
			return fmt.Errorf("load environment: %w", err)
		}

		if jsonOutput {
			out, _ := json.MarshalIndent(env.Hosts, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		fmt.Printf("Hosts in %s environment:\n", envName)
		for _, host := range env.Hosts {
			roleInfo := host.K3s.Role
			if host.K3s.ClusterInit {
				roleInfo += ", clusterInit"
			}
			modules := ""
			if len(host.Modules) > 0 {
				modules = fmt.Sprintf(" [%s]", strings.Join(host.Modules, ", "))
			}
			fmt.Printf("  %-12s (%s) - %s%s\n", host.Name, host.IP, roleInfo, modules)
		}
		return nil
	},
}

var hostBootstrapCmd = &cobra.Command{
	Use:   "bootstrap <hostname>",
	Short: "Bootstrap a new host",
	Long: `Bootstrap a new host from scratch using nixos-anywhere.

This command will:
1. Connect to the target machine via SSH
2. Partition and format disks according to disko configuration
3. Install NixOS with the host's configuration
4. Reboot into the new system

Requirements:
- Target must be booted into a NixOS installer ISO
- SSH access to root@<ip> must work
- Host configuration must exist in flake.nix`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		ip, _ := cmd.Flags().GetString("ip")
		generateHardware, _ := cmd.Flags().GetBool("generate-hardware")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if ip == "" {
			// Try to get IP from config
			configDir := getConfigDir()
			env, err := config.LoadEnvironment(configDir, "production")
			if err == nil {
				for _, host := range env.Hosts {
					if host.Name == hostname {
						ip = host.IP
						break
					}
				}
			}
			if ip == "" {
				return fmt.Errorf("IP address required: use --ip flag or ensure host is in config")
			}
		}

		if !jsonOutput {
			fmt.Printf("Bootstrapping %s at %s\n", hostname, ip)
			if dryRun {
				fmt.Println("(dry-run mode)")
			}
		}

		// Build nixos-anywhere command
		nixosAnywhereArgs := []string{
			"--flake", fmt.Sprintf(".#%s", hostname),
		}

		if generateHardware {
			facterPath := fmt.Sprintf("nix/hosts/%s/facter.json", hostname)
			nixosAnywhereArgs = append(nixosAnywhereArgs,
				"--generate-hardware-config", "nixos-facter", facterPath)
		}

		if dryRun {
			nixosAnywhereArgs = append(nixosAnywhereArgs, "--dry-run")
		}

		nixosAnywhereArgs = append(nixosAnywhereArgs, fmt.Sprintf("root@%s", ip))

		if verbose {
			fmt.Printf("Running: nixos-anywhere %s\n", strings.Join(nixosAnywhereArgs, " "))
		}

		nixosAnywhereCmd := exec.Command("nixos-anywhere", nixosAnywhereArgs...)
		nixosAnywhereCmd.Stdout = os.Stdout
		nixosAnywhereCmd.Stderr = os.Stderr
		nixosAnywhereCmd.Stdin = os.Stdin

		if err := nixosAnywhereCmd.Run(); err != nil {
			return fmt.Errorf("bootstrap failed: %w", err)
		}

		if !jsonOutput && !dryRun {
			fmt.Printf("\nBootstrap complete! Host %s should be rebooting into NixOS.\n", hostname)
			fmt.Println("Wait a minute, then run: lab host deploy", hostname)
		}

		return nil
	},
}

var hostSSHCmd = &cobra.Command{
	Use:   "ssh <hostname> [command...]",
	Short: "SSH to a host",
	Long:  `Open an SSH connection to the specified host, or run a command remotely.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]

		// Get IP from config if hostname doesn't resolve
		configDir := getConfigDir()
		env, err := config.LoadEnvironment(configDir, "production")
		target := hostname
		if err == nil {
			for _, host := range env.Hosts {
				if host.Name == hostname {
					target = host.IP
					break
				}
			}
		}

		sshArgs := []string{target}
		if len(args) > 1 {
			sshArgs = append(sshArgs, args[1:]...)
		}

		sshCmd := exec.Command("ssh", sshArgs...)
		sshCmd.Stdout = os.Stdout
		sshCmd.Stderr = os.Stderr
		sshCmd.Stdin = os.Stdin
		return sshCmd.Run()
	},
}

// validateHost checks if a hostname exists in the flake
func validateHost(hostname string) (string, error) {
	projectRoot, err := paths.RepoRoot()
	if err != nil {
		return "", err
	}
	// Check if the nixos configuration exists in the flake
	evalCmd := exec.Command("nix", "eval",
		fmt.Sprintf(".#nixosConfigurations.%s", hostname),
		"--apply", "x: x.config.system.stateVersion",
		"--raw")
	evalCmd.Stderr = nil // Suppress error output for validation
	evalCmd.Dir = projectRoot

	if err := evalCmd.Run(); err != nil {
		return "", fmt.Errorf("host %q not found in flake.nix nixosConfigurations", hostname)
	}
	return projectRoot, nil
}

// getChangedHosts returns hosts that have changes based on git diff
func getChangedHosts() ([]string, error) {
	// Get list of changed files
	gitCmd := exec.Command("git", "diff", "--name-only", "HEAD~1")
	output, err := gitCmd.Output()
	if err != nil {
		return nil, err
	}

	changedFiles := strings.Split(strings.TrimSpace(string(output)), "\n")
	hostSet := make(map[string]bool)

	for _, file := range changedFiles {
		// Check if file is in nix/hosts/<hostname>/
		if strings.HasPrefix(file, "nix/hosts/") {
			parts := strings.Split(file, "/")
			if len(parts) >= 3 && parts[2] != "common" {
				hostSet[parts[2]] = true
			}
		}
		// Check if file is in nix/modules/ (affects all hosts)
		if strings.HasPrefix(file, "nix/modules/") {
			// Return all hosts
			configDir := getConfigDir()
			env, err := config.LoadEnvironment(configDir, "production")
			if err != nil {
				return nil, err
			}
			var hosts []string
			for _, h := range env.Hosts {
				hosts = append(hosts, h.Name)
			}
			return hosts, nil
		}
	}

	var hosts []string
	for host := range hostSet {
		hosts = append(hosts, host)
	}
	return hosts, nil
}

var hostChangedCmd = &cobra.Command{
	Use:   "changed",
	Short: "List hosts with pending changes",
	Long:  `Show hosts that have configuration changes based on git diff.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		hosts, err := getChangedHosts()
		if err != nil {
			return fmt.Errorf("detect changes: %w", err)
		}

		if len(hosts) == 0 {
			if !jsonOutput {
				fmt.Println("No hosts with changes detected")
			} else {
				fmt.Println("[]")
			}
			return nil
		}

		if jsonOutput {
			out, _ := json.Marshal(hosts)
			fmt.Println(string(out))
		} else {
			fmt.Println("Hosts with changes:")
			for _, h := range hosts {
				fmt.Printf("  - %s\n", h)
			}
		}
		return nil
	},
}

var hostRebootCmd = &cobra.Command{
	Use:   "reboot [hostname...]",
	Short: "Reboot one or more hosts",
	Long: `Reboot hosts by creating a sentinel file for kured to orchestrate the reboot.
If no hostname is specified, reboot all hosts in the current environment.
Use --now to reboot immediately instead of waiting for kured.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		now, _ := cmd.Flags().GetBool("now")
		var hosts []string

		// If no hosts specified, get all hosts from environment
		if len(args) == 0 {
			envName, _ := cmd.Flags().GetString("env")
			configDir := getConfigDir()
			env, err := config.LoadEnvironment(configDir, envName)
			if err != nil {
				return fmt.Errorf("load environment: %w", err)
			}
			for _, host := range env.Hosts {
				hosts = append(hosts, host.Name)
			}
		} else {
			hosts = args
		}

		if len(hosts) == 0 {
			return fmt.Errorf("no hosts to reboot")
		}

		// Get host IPs from config
		configDir := getConfigDir()
		env, err := config.LoadEnvironment(configDir, "production")
		if err != nil {
			return fmt.Errorf("load environment: %w", err)
		}

		hostIPs := make(map[string]string)
		for _, host := range env.Hosts {
			hostIPs[host.Name] = host.IP
		}

		var rebootCmd string
		var action string
		if now {
			rebootCmd = "sudo reboot"
			action = "Rebooting"
		} else {
			rebootCmd = "sudo touch /var/run/reboot-required"
			action = "Scheduling reboot for"
		}

		for _, hostname := range hosts {
			target := hostname
			if ip, ok := hostIPs[hostname]; ok {
				target = ip
			}

			if !jsonOutput {
				fmt.Printf("%s %s...\n", action, hostname)
			}

			sshCmd := exec.Command("ssh", target, rebootCmd)
			if err := sshCmd.Run(); err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Failed to reboot %s: %v\n", hostname, err)
				}
				continue
			}

			if !jsonOutput && !now {
				fmt.Printf("Created reboot sentinel for %s\n", hostname)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(hostCmd)

	hostBuildCmd.Flags().Bool("show-trace", false, "Show detailed error traces")
	hostCmd.AddCommand(hostBuildCmd)

	hostDeployCmd.Flags().Bool("dry-run", false, "Perform a dry run without making changes")
	hostDeployCmd.Flags().Bool("skip-checks", false, "Skip deploy-rs checks")
	hostDeployCmd.Flags().Bool("boot", false, "Activate the deployment on next boot")
	hostCmd.AddCommand(hostDeployCmd)

	hostCmd.AddCommand(hostDiffCmd)

	hostListCmd.Flags().String("env", "production", "Environment to list hosts from")
	hostCmd.AddCommand(hostListCmd)

	hostBootstrapCmd.Flags().String("ip", "", "IP address of the new host")
	hostBootstrapCmd.Flags().Bool("generate-hardware", true, "Generate hardware config with nixos-facter")
	hostBootstrapCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	hostCmd.AddCommand(hostBootstrapCmd)

	hostCmd.AddCommand(hostSSHCmd)
	hostCmd.AddCommand(hostChangedCmd)

	hostRebootCmd.Flags().Bool("now", false, "Reboot immediately instead of creating sentinel file")
	hostRebootCmd.Flags().String("env", "production", "Environment to load hosts from (when no hostname specified)")
	hostCmd.AddCommand(hostRebootCmd)
}
