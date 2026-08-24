package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
)

func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Manage NixOS hosts",
		Long:  `Commands for building, deploying, and managing NixOS hosts.`,
	}

	cmd.AddCommand(newHostBuildCmd())
	cmd.AddCommand(newHostDeployCmd())
	cmd.AddCommand(newHostDiffCmd())
	cmd.AddCommand(newHostListCmd())
	cmd.AddCommand(newHostBootstrapCmd())
	cmd.AddCommand(newHostSSHCmd())
	cmd.AddCommand(newHostChangedCmd())
	cmd.AddCommand(newHostRebootCmd())
	cmd.AddCommand(newHostGenerationsCmd())

	return cmd
}

func newHostBuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build <hostname>",
		Short: "Build NixOS configuration for a host",
		Long:  `Build the NixOS configuration for the specified host without deploying.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]
			showTrace, _ := cmd.Flags().GetBool("show-trace")

			repoRoot, err := validateHost(cmd.Context(), hostname)
			if err != nil {
				return err
			}

			if !jsonOutput {
				fmt.Printf("Building NixOS configuration for %s...\n", hostname)
			}

			nixArgs := []string{
				"build",
				fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
				"--no-link",
				"--print-out-paths",
			}
			if showTrace {
				nixArgs = append(nixArgs, "--show-trace")
			}

			nixCmd := exec.CommandContext(cmd.Context(), "nix", nixArgs...)
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

	cmd.Flags().Bool("show-trace", false, "Show detailed error traces")

	return cmd
}

func newHostDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy <hostname>",
		Short: "Deploy NixOS configuration to a host",
		Long:  `Deploy the NixOS configuration to the specified host using deploy-rs.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			skipChecks, _ := cmd.Flags().GetBool("skip-checks")
			boot, _ := cmd.Flags().GetBool("boot")
			kuredReboot, _ := cmd.Flags().GetBool("kured-reboot")

			repoRoot, err := validateHost(cmd.Context(), hostname)
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

			deployArgs := buildDeployArgs(hostname, skipChecks, dryRun, boot || kuredReboot)

			deployCmd := exec.CommandContext(cmd.Context(), "deploy", deployArgs...)
			deployCmd.Stdout = os.Stdout
			deployCmd.Stderr = os.Stderr
			deployCmd.Stdin = os.Stdin
			deployCmd.Dir = repoRoot

			if err := deployCmd.Run(); err != nil {
				return fmt.Errorf("deploy failed: %w", err)
			}

			if kuredReboot && !dryRun {
				if err := createKuredRebootSentinel(cmd.Context(), hostname, repoRoot); err != nil {
					return err
				}
				fmt.Println("Successfully created kured reboot sentinel file")
			}

			if jsonOutput {
				result := map[string]any{
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

	cmd.Flags().Bool("dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().Bool("skip-checks", false, "Skip deploy-rs checks")
	cmd.Flags().Bool("boot", false, "Activate the system on next boot")
	cmd.Flags().Bool("kured-reboot", false, "Reboot into new system using kured (implies --boot)")

	return cmd
}

// buildDeployArgs constructs the deploy-rs argument list for a deploy to hostname.
func buildDeployArgs(hostname string, skipChecks, dryRun, boot bool) []string {
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
	return deployArgs
}

// createKuredRebootSentinel touches the kured reboot-required sentinel file on hostname.
func createKuredRebootSentinel(ctx context.Context, hostname, repoRoot string) error {
	rebootCmd := exec.CommandContext(ctx, "ssh", hostname, "sudo", "touch", "/run/reboot-required")
	rebootCmd.Stdout = os.Stdout
	rebootCmd.Stderr = os.Stderr
	rebootCmd.Stdin = os.Stdin
	rebootCmd.Dir = repoRoot
	if err := rebootCmd.Run(); err != nil {
		return fmt.Errorf("creating reboot sentinel file failed: %w", err)
	}
	return nil
}

func newHostDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <hostname>",
		Short: "Show pending changes for a host",
		Long: `Show what would change if the host configuration was deployed.
Uses nvd (nix-visualize-derivation) to show a human-readable diff.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]

			repoRoot, err := validateHost(cmd.Context(), hostname)
			if err != nil {
				return err
			}

			if !jsonOutput {
				fmt.Printf("Computing diff for %s...\n", hostname)
			}

			newPath, err := buildHostSystemClosure(cmd.Context(), hostname, repoRoot)
			if err != nil {
				return err
			}

			currentPath, reachable := currentHostSystemClosure(cmd.Context(), hostname)
			if !reachable {
				if !jsonOutput {
					fmt.Printf("Cannot reach %s, showing build output:\n", hostname)
					fmt.Println(newPath)
				}
				return nil
			}

			if currentPath == newPath {
				printNoHostChanges(hostname)
				return nil
			}

			if !jsonOutput {
				fmt.Printf("Fetching current system closure from %s...\n", hostname)
			}
			if err := copyHostSystemClosure(cmd.Context(), hostname, currentPath); err != nil {
				return err
			}

			if !jsonOutput {
				fmt.Printf("Changes from %s to %s:\n\n", currentPath, newPath)
			}

			return printHostSystemDiff(cmd.Context(), currentPath, newPath)
		},
	}
}

// buildHostSystemClosure builds the NixOS system closure for hostname and returns its store path.
func buildHostSystemClosure(ctx context.Context, hostname, repoRoot string) (string, error) {
	nixBuildArgs := []string{
		"build",
		fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", hostname),
		"--no-link",
		"--print-out-paths",
	}

	nixCmd := exec.CommandContext(ctx, "nix", nixBuildArgs...)
	nixCmd.Stderr = os.Stderr
	nixCmd.Dir = repoRoot
	newPathBytes, err := nixCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to build new configuration: %w", err)
	}
	return strings.TrimSpace(string(newPathBytes)), nil
}

// currentHostSystemClosure reads the active system closure path from hostname over SSH.
// The second return value is false if the host could not be reached.
func currentHostSystemClosure(ctx context.Context, hostname string) (string, bool) {
	sshCmd := exec.CommandContext(ctx, "ssh", hostname, "readlink", "-f", "/run/current-system")
	currentPathBytes, err := sshCmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(currentPathBytes)), true
}

// printNoHostChanges prints a "no changes" result for hostname in the appropriate output format.
func printNoHostChanges(hostname string) {
	if !jsonOutput {
		fmt.Println("No changes - system is up to date")
		return
	}
	result := map[string]any{
		"host":    hostname,
		"changed": false,
	}
	out, _ := json.Marshal(result)
	fmt.Println(string(out))
}

// copyHostSystemClosure copies the system closure at path from hostname into the local store.
func copyHostSystemClosure(ctx context.Context, hostname, path string) error {
	copyCmd := exec.CommandContext(ctx, "nix", "copy",
		"--from", "ssh://"+hostname,
		"--no-check-sigs",
		path)
	copyCmd.Stdout = os.Stderr
	copyCmd.Stderr = os.Stderr
	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("copy current system closure from %s: %w", hostname, err)
	}
	return nil
}

// printHostSystemDiff prints a human-readable diff between two system closures,
// preferring nvd and falling back to `nix store diff-closures`.
func printHostSystemDiff(ctx context.Context, currentPath, newPath string) error {
	nvdCmd := exec.CommandContext(ctx, "nvd", "diff", currentPath, newPath)
	nvdCmd.Stdout = os.Stdout
	nvdCmd.Stderr = os.Stderr
	if err := nvdCmd.Run(); err != nil {
		nixDiffCmd := exec.CommandContext(ctx, "nix", "store", "diff-closures", currentPath, newPath)
		nixDiffCmd.Stdout = os.Stdout
		nixDiffCmd.Stderr = os.Stderr
		if err := nixDiffCmd.Run(); err != nil {
			return fmt.Errorf("nix store diff-closures: %w", err)
		}
	}
	return nil
}

func newHostListCmd() *cobra.Command {
	cmd := &cobra.Command{
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

	cmd.Flags().String("env", "production", "Environment to list hosts from")

	return cmd
}

// newHostBootstrapCmd is defined in host_bootstrap.go

func newHostSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh <hostname> [command...]",
		Short: "SSH to a host",
		Long:  `Open an SSH connection to the specified host, or run a command remotely.`,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]

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

			sshCmd := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
			sshCmd.Stdout = os.Stdout
			sshCmd.Stderr = os.Stderr
			sshCmd.Stdin = os.Stdin
			return sshCmd.Run()
		},
	}
}

func newHostChangedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "changed",
		Short: "List hosts with pending changes",
		Long:  `Show hosts that have configuration changes based on git diff.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := getChangedHosts(cmd.Context())
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
}

func newHostRebootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reboot [hostname...]",
		Short: "Reboot one or more hosts",
		Long: `Reboot hosts by creating a sentinel file for kured to orchestrate the reboot.
If no hostname is specified, reboot all hosts in the current environment.
Use --now to reboot immediately instead of waiting for kured.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			now, _ := cmd.Flags().GetBool("now")

			hosts, err := resolveRebootHosts(cmd, args)
			if err != nil {
				return err
			}
			if len(hosts) == 0 {
				return fmt.Errorf("no hosts to reboot")
			}

			hostIPs, err := loadHostIPs(getConfigDir())
			if err != nil {
				return err
			}

			rebootCmd, action := "sudo touch /run/reboot-required", "Scheduling reboot for"
			if now {
				rebootCmd, action = "sudo reboot", "Rebooting"
			}

			for _, hostname := range hosts {
				rebootHost(cmd.Context(), hostname, hostIPs[hostname], rebootCmd, action, now)
			}

			return nil
		},
	}

	cmd.Flags().Bool("now", false, "Reboot immediately instead of creating sentinel file")
	cmd.Flags().String("env", "production", "Environment to load hosts from (when no hostname specified)")

	return cmd
}

// resolveRebootHosts returns the hostnames to reboot: the given args if any,
// otherwise every host in the environment named by the command's --env flag.
func resolveRebootHosts(cmd *cobra.Command, args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}

	envName, _ := cmd.Flags().GetString("env")
	env, err := config.LoadEnvironment(getConfigDir(), envName)
	if err != nil {
		return nil, fmt.Errorf("load environment: %w", err)
	}

	var hosts []string
	for _, host := range env.Hosts {
		hosts = append(hosts, host.Name)
	}
	return hosts, nil
}

// loadHostIPs returns a map of host name to IP address for the production environment.
func loadHostIPs(configDir string) (map[string]string, error) {
	env, err := config.LoadEnvironment(configDir, "production")
	if err != nil {
		return nil, fmt.Errorf("load environment: %w", err)
	}

	hostIPs := make(map[string]string)
	for _, host := range env.Hosts {
		hostIPs[host.Name] = host.IP
	}
	return hostIPs, nil
}

// rebootHost triggers a reboot (or reboot-sentinel) on a single host over SSH,
// preferring its IP if known, and prints progress/errors. Errors are logged, not returned,
// so one unreachable host doesn't stop the rest of the batch.
func rebootHost(ctx context.Context, hostname, ip, rebootCmd, action string, now bool) {
	target := hostname
	if ip != "" {
		target = ip
	}

	if !jsonOutput {
		fmt.Printf("%s %s...\n", action, hostname)
	}

	sshCmd := exec.CommandContext(ctx, "ssh", target, rebootCmd)
	if err := sshCmd.Run(); err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Failed to reboot %s: %v\n", hostname, err)
		}
		return
	}

	if !jsonOutput && !now {
		fmt.Printf("Created reboot sentinel for %s\n", hostname)
	}
}

func validateHost(ctx context.Context, hostname string) (string, error) {
	projectRoot, err := paths.RepoRoot()
	if err != nil {
		return "", fmt.Errorf("finding repo root: %w", err)
	}
	evalCmd := exec.CommandContext(ctx, "nix", "eval",
		fmt.Sprintf(".#nixosConfigurations.%s", hostname),
		"--apply", "x: x.config.system.stateVersion",
		"--raw")
	evalCmd.Stderr = nil
	evalCmd.Dir = projectRoot

	if err := evalCmd.Run(); err != nil {
		return "", fmt.Errorf("host %q not found in flake.nix nixosConfigurations", hostname)
	}
	return projectRoot, nil
}

func getChangedHosts(ctx context.Context) ([]string, error) {
	gitCmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD~1")
	output, err := gitCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("getting changed files: %w", err)
	}

	changedFiles := strings.Split(strings.TrimSpace(string(output)), "\n")
	hostSet := make(map[string]bool)

	for _, file := range changedFiles {
		if strings.HasPrefix(file, "nix/hosts/") {
			parts := strings.Split(file, "/")
			if len(parts) >= 3 && parts[2] != "common" {
				hostSet[parts[2]] = true
			}
		}
		if strings.HasPrefix(file, "nix/modules/") {
			configDir := getConfigDir()
			env, err := config.LoadEnvironment(configDir, "production")
			if err != nil {
				return nil, fmt.Errorf("loading production environment: %w", err)
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
