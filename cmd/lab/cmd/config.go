package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
)

var (
	envName string
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CUE configuration",
	Long:  `Commands for working with the CUE-based environment configuration.`,
}

var configShowCmd = &cobra.Command{
	Use:   "show [environment]",
	Short: "Show configuration for an environment",
	Long: `Display the resolved configuration for an environment.
If no environment is specified, shows the base configuration.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		configDir := getConfigDir()

		env := "base"
		if len(args) > 0 {
			env = args[0]
		}

		cfg, err := config.LoadEnvironment(configDir, env)
		if err != nil {
			return fmt.Errorf("load environment %q: %w", env, err)
		}

		if jsonOutput {
			out, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}
			fmt.Println(string(out))
		} else {
			// Pretty print for terminal
			fmt.Printf("Environment: %s\n", cfg.Name)
			fmt.Printf("Domain: %s\n", cfg.Cluster.Domain)
			fmt.Printf("Timezone: %s\n", cfg.Cluster.Timezone)
			fmt.Printf("\nNetworks:\n")
			fmt.Printf("  Host CIDR: %s\n", cfg.Cluster.Networks.HostCIDR)
			fmt.Printf("  Pod CIDR: %s\n", cfg.Cluster.Networks.PodCIDR)
			fmt.Printf("  Service CIDR: %s\n", cfg.Cluster.Networks.ServiceCIDR)
			fmt.Printf("\nHosts (%d):\n", len(cfg.Hosts))
			for _, h := range cfg.Hosts {
				fmt.Printf("  - %s (%s) role=%s\n", h.Name, h.IP, h.K3s.Role)
			}
			fmt.Printf("\nApps:\n")
			fmt.Printf("  Foundation: %v\n", cfg.Apps.Foundation)
			fmt.Printf("  Platform: %v\n", cfg.Apps.Platform)
			fmt.Printf("  Apps: %v\n", cfg.Apps.Apps)
		}
		return nil
	},
}

var configValidateCmd = &cobra.Command{
	Use:   "validate [environment]",
	Short: "Validate configuration",
	Long: `Validate the CUE configuration for an environment.
If no environment is specified, validates all environments.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		configDir := getConfigDir()

		if len(args) > 0 {
			env := args[0]
			if err := config.ValidateEnvironment(configDir, env); err != nil {
				return fmt.Errorf("validation failed for %q: %w", env, err)
			}
			fmt.Printf("Environment %q is valid\n", env)
			return nil
		}

		// Validate all environments
		entries, err := os.ReadDir(configDir)
		if err != nil {
			return fmt.Errorf("read config directory: %w", err)
		}

		// Exclude non-environment files
		excludeFiles := map[string]bool{
			"schema.cue": true,
			"base.cue":   true,
		}

		var errors []error
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".cue" && !excludeFiles[entry.Name()] {
				env := entry.Name()[:len(entry.Name())-4] // strip .cue
				if err := config.ValidateEnvironment(configDir, env); err != nil {
					errors = append(errors, fmt.Errorf("%s: %w", env, err))
				} else {
					fmt.Printf("Environment %q is valid\n", env)
				}
			}
		}

		if len(errors) > 0 {
			fmt.Fprintf(os.Stderr, "\nValidation errors:\n")
			for _, err := range errors {
				fmt.Fprintf(os.Stderr, "  - %v\n", err)
			}
			return fmt.Errorf("%d environment(s) failed validation", len(errors))
		}
		return nil
	},
}

var configExportCmd = &cobra.Command{
	Use:   "export <environment> <format>",
	Short: "Export configuration to different formats",
	Long: `Export the environment configuration to different formats.
Supported formats: json, yaml, nix, helm, terraform`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		configDir := getConfigDir()
		env := args[0]
		format := args[1]

		output, err := config.ExportEnvironment(configDir, env, format)
		if err != nil {
			return fmt.Errorf("export environment %q to %s: %w", env, format, err)
		}
		fmt.Print(output)
		return nil
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available environments",
	Long:  `List all available environment configurations.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		configDir := getConfigDir()

		entries, err := os.ReadDir(configDir)
		if err != nil {
			return fmt.Errorf("read config directory: %w", err)
		}

		// Filter for environment files (exclude schema.cue and base.cue)
		excludeFiles := map[string]bool{
			"schema.cue": true,
			"base.cue":   true,
		}

		if jsonOutput {
			var envs []string
			for _, entry := range entries {
				if !entry.IsDir() && filepath.Ext(entry.Name()) == ".cue" && !excludeFiles[entry.Name()] {
					envs = append(envs, entry.Name()[:len(entry.Name())-4])
				}
			}
			out, _ := json.Marshal(envs)
			fmt.Println(string(out))
		} else {
			fmt.Println("Available environments:")
			for _, entry := range entries {
				if !entry.IsDir() && filepath.Ext(entry.Name()) == ".cue" && !excludeFiles[entry.Name()] {
					fmt.Printf("  - %s\n", entry.Name()[:len(entry.Name())-4])
				}
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configValidateCmd)
	configCmd.AddCommand(configExportCmd)
	configCmd.AddCommand(configListCmd)
}
