package cmd

import (
	"fmt"
	"os"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/env"
)

var (
	envMgr     *env.Manager
	envMgrOnce sync.Once
)

func getEnvManager() *env.Manager {
	envMgrOnce.Do(func() {
		envMgr = env.NewManager() // Uses XDG defaults
	})
	return envMgr
}

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environments",
	Long:  `Commands for creating, starting, stopping, and managing environments.`,
}

var envCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new environment",
	Long: `Create a new Kind-based staging or ephemeral environment.

Examples:
  lab env create staging              # Create staging environment
  lab env create pr-123 --from staging --workers 2  # Create with 2 worker nodes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		fromEnv, _ := cmd.Flags().GetString("from")
		workers, _ := cmd.Flags().GetInt("workers")
		cmd.SilenceUsage = true

		fmt.Printf("Creating environment %q...\n", name)

		e, err := mgr.Create(name, fromEnv, workers)
		if err != nil {
			return fmt.Errorf("create environment: %w", err)
		}

		fmt.Printf("Environment %q created successfully.\n", name)
		fmt.Printf("  Type:       %s\n", e.Type)
		fmt.Printf("  Status:     %s\n", e.Status)
		fmt.Printf("  Kubeconfig: %s\n", e.Config.Kubeconfig)
		fmt.Println("\nTo use this environment:")
		fmt.Printf("  export KUBECONFIG=%s\n", e.Config.Kubeconfig)
		return nil
	},
}

var envStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start an environment",
	Long: `Start a previously created environment.

If the Kind cluster was deleted, it will be recreated using the saved configuration.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		cmd.SilenceUsage = true

		fmt.Printf("Starting environment %q...\n", name)

		if err := mgr.Start(name); err != nil {
			return fmt.Errorf("start environment: %w", err)
		}

		fmt.Printf("Environment %q started successfully.\n", name)

		e, _ := mgr.Get(name)
		if e != nil && e.Config.Kubeconfig != "" {
			fmt.Println("\nTo use this environment:")
			fmt.Printf("  export KUBECONFIG=%s\n", e.Config.Kubeconfig)
		}
		return nil
	},
}

var envStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop an environment",
	Long: `Stop a running environment.

By default, the Kind cluster is deleted but state is preserved so it can be recreated.
Use --preserve-state to keep the cluster running but mark it as stopped.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		preserveState, _ := cmd.Flags().GetBool("preserve-state")
		cmd.SilenceUsage = true

		fmt.Printf("Stopping environment %q...\n", name)

		if err := mgr.Stop(name, preserveState); err != nil {
			return fmt.Errorf("stop environment: %w", err)
		}

		fmt.Printf("Environment %q stopped successfully.\n", name)
		return nil
	},
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environments",
	Long:  `List all configured and running environments.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		cmd.SilenceUsage = true

		envs, err := mgr.List()
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}

		if jsonOutput {
			return printJSON(envs)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tSTATUS\tFROM")
		for _, e := range envs {
			from := e.FromEnv
			if from == "" {
				from = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, e.Type, e.Status, from)
		}
		return w.Flush()
	},
}

var envDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an environment",
	Long: `Delete an environment and all its state.

This will delete the Kind cluster (if running) and remove all state files.
This action cannot be undone.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		force, _ := cmd.Flags().GetBool("force")
		cmd.SilenceUsage = true

		// Prevent deleting production
		if name == "production" {
			return fmt.Errorf("cannot delete production environment")
		}
		if !force {
			fmt.Printf("Are you sure you want to delete environment %q? This cannot be undone.\n", name)
			fmt.Print("Type 'yes' to confirm: ")
			var confirm string
			fmt.Scanln(&confirm)
			if confirm != "yes" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		fmt.Printf("Deleting environment %q...\n", name)

		if err := mgr.Delete(name); err != nil {
			return fmt.Errorf("delete environment: %w", err)
		}

		fmt.Printf("Environment %q deleted successfully.\n", name)
		return nil
	},
}

var envStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Show environment status",
	Long:  `Show detailed status information for an environment.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		cmd.SilenceUsage = true

		e, err := mgr.Get(name)
		if err != nil {
			return fmt.Errorf("get environment: %w", err)
		}

		if jsonOutput {
			return printJSON(e)
		}

		fmt.Printf("Name:       %s\n", e.Name)
		fmt.Printf("Type:       %s\n", e.Type)
		fmt.Printf("Status:     %s\n", e.Status)
		if e.FromEnv != "" {
			fmt.Printf("From:       %s\n", e.FromEnv)
		}
		if !e.CreatedAt.IsZero() {
			fmt.Printf("Created:    %s\n", e.CreatedAt.Format("2006-01-02 15:04:05"))
		}
		if !e.UpdatedAt.IsZero() {
			fmt.Printf("Updated:    %s\n", e.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
		if e.Config.Kubeconfig != "" {
			fmt.Printf("Kubeconfig: %s\n", e.Config.Kubeconfig)
		}
		if e.Config.Workers > 0 {
			fmt.Printf("Workers:    %d\n", e.Config.Workers)
		}

		return nil
	},
}

var envKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig <name>",
	Short: "Show kubeconfig path for an environment",
	Long:  `Show the kubeconfig path for an environment.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getEnvManager()
		name := args[0]
		cmd.SilenceUsage = true

		kubeconfig, err := mgr.GetKubeconfig(name)
		if err != nil {
			return fmt.Errorf("get kubeconfig: %w", err)
		}

		fmt.Println(kubeconfig)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)

	envCreateCmd.Flags().String("from", "production", "Environment to clone configuration from")
	envCreateCmd.Flags().Int("workers", 0, "Number of worker nodes (default: 0 for single-node)")
	envCmd.AddCommand(envCreateCmd)

	envCmd.AddCommand(envStartCmd)

	envStopCmd.Flags().Bool("preserve-state", false, "Don't delete the Kind cluster, just mark as stopped")
	envCmd.AddCommand(envStopCmd)

	envCmd.AddCommand(envListCmd)

	envDeleteCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	envCmd.AddCommand(envDeleteCmd)

	envCmd.AddCommand(envStatusCmd)
	envCmd.AddCommand(envKubeconfigCmd)
}
