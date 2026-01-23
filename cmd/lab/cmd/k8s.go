package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
	"github.com/teekennedy/homelab/cmd/lab/kubeconfig"
)

var (
	kubeconfigMgr     *kubeconfig.Manager
	kubeconfigMgrOnce sync.Once
)

// getKubeconfigManager returns the singleton kubeconfig manager
func getKubeconfigManager() *kubeconfig.Manager {
	kubeconfigMgrOnce.Do(func() {
		kubeconfigMgr = kubeconfig.NewManager(
			kubeconfig.WithConfigDir(paths.ProjectConfigDir()),
		)
	})
	return kubeconfigMgr
}

// setupKubeconfig sets up the kubeconfig for the given environment
// Returns a cleanup function that must be called when done
func setupKubeconfig(envName string) (func(), error) {
	mgr := getKubeconfigManager()

	// Check if kubeconfig exists for this environment
	if !mgr.Exists(envName) {
		return nil, fmt.Errorf("no kubeconfig found for environment %q (expected at %s)", envName, mgr.GetEncryptedPath(envName))
	}

	cleanup, err := mgr.Setup(envName)
	if err != nil {
		return nil, fmt.Errorf("setup kubeconfig: %w", err)
	}

	return cleanup, nil
}

var k8sCmd = &cobra.Command{
	Use:   "k8s",
	Short: "Kubernetes operations",
	Long: `Commands for managing Kubernetes resources and applications.

Kubeconfig files are stored encrypted per environment in config/kubeconfig/<env>.enc.yaml
and are automatically decrypted using sops when needed.`,
}

var k8sBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap Kubernetes cluster",
	Long: `Bootstrap the Kubernetes cluster with foundation components.

This command installs the foundation tier applications in the correct order:
1. secret-system (SOPS secrets operator)
2. cert-system (cert-manager)
3. metallb (load balancer)
4. argocd (GitOps controller)
5. Remaining foundation apps

After bootstrap, ArgoCD will manage all applications.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		skipArgo, _ := cmd.Flags().GetBool("skip-argocd")

		configDir := getConfigDir()
		env, err := config.LoadEnvironment(configDir, envName)
		if err != nil {
			return fmt.Errorf("load environment: %w", err)
		}

		// Setup kubeconfig for the environment
		cleanup, err := setupKubeconfig(envName)
		if err != nil {
			return err
		}
		defer cleanup()

		if !jsonOutput {
			fmt.Printf("Bootstrapping Kubernetes cluster for %s environment\n", envName)
			if dryRun {
				fmt.Println("(dry-run mode)")
			}
		}

		// Bootstrap order - these must be installed first
		bootstrapOrder := []string{
			"secret-system",
			"cert-system",
			"metallb",
		}

		if !skipArgo {
			bootstrapOrder = append(bootstrapOrder, "argocd")
		}

		// Install bootstrap components via helmfile
		for _, app := range bootstrapOrder {
			if !contains(env.Apps.Foundation, app) {
				continue
			}

			appPath := filepath.Join("k8s/foundation", app)
			if _, err := os.Stat(appPath); os.IsNotExist(err) {
				continue
			}

			if !jsonOutput {
				fmt.Printf("\nInstalling %s...\n", app)
			}

			helmfilePath := filepath.Join(appPath, "helmfile.yaml")
			if _, err := os.Stat(helmfilePath); os.IsNotExist(err) {
				// Try to apply kustomization or raw manifests
				if err := applyKustomization(appPath, dryRun); err != nil {
					return fmt.Errorf("install %s: %w", app, err)
				}
				continue
			}

			helmArgs := []string{"-f", helmfilePath}
			if dryRun {
				helmArgs = append(helmArgs, "diff")
			} else {
				helmArgs = append(helmArgs, "apply")
			}

			helmCmd := exec.Command("helmfile", helmArgs...)
			helmCmd.Stdout = os.Stdout
			helmCmd.Stderr = os.Stderr
			if err := helmCmd.Run(); err != nil {
				return fmt.Errorf("install %s: %w", app, err)
			}
		}

		// If ArgoCD is installed, apply the app-of-apps
		if !skipArgo && !dryRun {
			if !jsonOutput {
				fmt.Println("\nApplying ArgoCD app-of-apps...")
			}

			// Apply foundation app-of-apps
			kubectlCmd := exec.Command("kubectl", "apply", "-f", "k8s/foundation/application.yaml")
			kubectlCmd.Stdout = os.Stdout
			kubectlCmd.Stderr = os.Stderr
			if err := kubectlCmd.Run(); err != nil {
				fmt.Printf("Warning: failed to apply foundation app-of-apps: %v\n", err)
			}
		}

		if !jsonOutput && !dryRun {
			fmt.Println("\nBootstrap complete!")
			fmt.Println("ArgoCD will now manage the remaining applications.")
			fmt.Println("\nTo access ArgoCD UI:")
			fmt.Println("  kubectl port-forward svc/argocd-server -n argocd 8080:443")
			fmt.Println("  open https://localhost:8080")
		}

		return nil
	},
}

var k8sDiffCmd = &cobra.Command{
	Use:   "diff [app]",
	Short: "Show pending Kubernetes changes",
	Long: `Show what would change for Kubernetes resources.

Examples:
  lab k8s diff                    # Diff all tiers
  lab k8s diff foundation         # Diff entire foundation tier
  lab k8s diff platform/gitea     # Diff specific app
  lab k8s diff gitea              # Diff app (auto-detect tier)`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")

		// Setup kubeconfig for the environment
		cleanup, err := setupKubeconfig(envName)
		if err != nil {
			return err
		}
		defer cleanup()

		target := ""
		if len(args) > 0 {
			target = args[0]
		}

		tier, app := parseK8sTarget(target)

		if tier == "" && app == "" {
			// Diff all tiers
			for _, t := range []string{"foundation", "platform", "apps"} {
				if !jsonOutput {
					fmt.Printf("\n=== %s ===\n", strings.ToUpper(t))
				}
				if err := diffTier(t); err != nil {
					fmt.Printf("Warning: %v\n", err)
				}
			}
			return nil
		}

		if app == "" {
			// Diff entire tier
			return diffTier(tier)
		}

		// Diff specific app
		return diffApp(tier, app)
	},
}

var k8sSyncCmd = &cobra.Command{
	Use:   "sync [app]",
	Short: "Sync Kubernetes resources",
	Long: `Force sync Kubernetes resources via ArgoCD or Helmfile.

Examples:
  lab k8s sync foundation         # Sync entire foundation tier
  lab k8s sync platform/gitea     # Sync specific app
  lab k8s sync gitea --argocd     # Sync via ArgoCD`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")

		// Setup kubeconfig for the environment
		cleanup, err := setupKubeconfig(envName)
		if err != nil {
			return err
		}
		defer cleanup()

		target := ""
		if len(args) > 0 {
			target = args[0]
		}

		useArgo, _ := cmd.Flags().GetBool("argocd")
		prune, _ := cmd.Flags().GetBool("prune")

		tier, app := parseK8sTarget(target)

		if useArgo {
			return syncViaArgoCD(tier, app, prune)
		}

		if tier == "" && app == "" {
			return fmt.Errorf("please specify a tier or app to sync")
		}

		if app == "" {
			return syncTier(tier, prune)
		}

		return syncApp(tier, app, prune)
	},
}

var k8sListCmd = &cobra.Command{
	Use:   "list [tier]",
	Short: "List Kubernetes applications",
	Long: `List applications configured for Kubernetes deployment.

Examples:
  lab k8s list              # List all apps by tier
  lab k8s list foundation   # List foundation apps
  lab k8s list --env staging # List apps for staging environment`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")
		configDir := getConfigDir()

		env, err := config.LoadEnvironment(configDir, envName)
		if err != nil {
			return fmt.Errorf("load environment: %w", err)
		}

		tier := ""
		if len(args) > 0 {
			tier = args[0]
		}

		// Check if kubeconfig exists for environment
		mgr := getKubeconfigManager()
		hasKubeconfig := mgr.Exists(envName)

		if jsonOutput {
			var output interface{}
			if tier == "" {
				output = map[string]interface{}{
					"environment":   envName,
					"hasKubeconfig": hasKubeconfig,
					"apps":          env.Apps,
				}
			} else {
				switch tier {
				case "foundation":
					output = env.Apps.Foundation
				case "platform":
					output = env.Apps.Platform
				case "apps":
					output = env.Apps.Apps
				default:
					return fmt.Errorf("unknown tier: %s", tier)
				}
			}
			out, _ := json.MarshalIndent(output, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		fmt.Printf("Applications for %s environment", envName)
		if hasKubeconfig {
			fmt.Println(" (kubeconfig available)")
		} else {
			fmt.Println(" (no kubeconfig)")
		}

		printTier := func(name string, apps []string) {
			if tier != "" && tier != name {
				return
			}
			fmt.Printf("\n%s:\n", strings.Title(name))
			for _, app := range apps {
				// Check if app directory exists
				appPath := filepath.Join("k8s", name, app)
				status := "✓"
				if _, err := os.Stat(appPath); os.IsNotExist(err) {
					status = "✗ (missing)"
				}
				fmt.Printf("  %s %s\n", status, app)
			}
		}

		printTier("foundation", env.Apps.Foundation)
		printTier("platform", env.Apps.Platform)
		printTier("apps", env.Apps.Apps)

		return nil
	},
}

var k8sStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cluster status",
	Long:  `Show the status of the Kubernetes cluster and deployed applications.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")

		// Setup kubeconfig for the environment
		cleanup, err := setupKubeconfig(envName)
		if err != nil {
			return err
		}
		defer cleanup()

		if !jsonOutput {
			fmt.Printf("Cluster Status (%s environment):\n", envName)
		}

		// Get node status
		if !jsonOutput {
			fmt.Println("\nNodes:")
		}
		kubectlCmd := exec.Command("kubectl", "get", "nodes", "-o", "wide")
		kubectlCmd.Stdout = os.Stdout
		kubectlCmd.Stderr = os.Stderr
		if err := kubectlCmd.Run(); err != nil {
			return fmt.Errorf("get nodes: %w", err)
		}

		// Get ArgoCD app status
		if !jsonOutput {
			fmt.Println("\nArgoCD Applications:")
		}
		argoCmd := exec.Command("argocd", "app", "list", "--grpc-web")
		argoCmd.Stdout = os.Stdout
		argoCmd.Stderr = os.Stderr
		if err := argoCmd.Run(); err != nil {
			// Try kubectl if argocd CLI not available
			kubectlCmd := exec.Command("kubectl", "get", "applications", "-n", "argocd",
				"-o", "custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status")
			kubectlCmd.Stdout = os.Stdout
			kubectlCmd.Stderr = os.Stderr
			kubectlCmd.Run()
		}

		return nil
	},
}

var k8sGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate cluster values from CUE config",
	Long: `Generate Helm values and other configuration files from the CUE environment configuration.

This exports the environment configuration to formats usable by Helm charts.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		envName, _ := cmd.Flags().GetString("env")
		outputDir, _ := cmd.Flags().GetString("output")
		configDir := getConfigDir()

		env, err := config.LoadEnvironment(configDir, envName)
		if err != nil {
			return fmt.Errorf("load environment: %w", err)
		}

		if outputDir == "" {
			outputDir = filepath.Join(configDir, "gen")
		}

		// Create output directory
		if outputDirErr := os.MkdirAll(outputDir, 0o755); outputDirErr != nil {
			return fmt.Errorf("create output directory: %w", outputDirErr)
		}

		// Generate cluster-wide Helm values
		helmValues, err := config.ExportEnvironment(configDir, envName, "helm")
		if err != nil {
			return fmt.Errorf("export helm values: %w", err)
		}

		helmPath := filepath.Join(outputDir, "cluster-values.yaml")
		if clusterValuesErr := os.WriteFile(helmPath, []byte(helmValues), 0o644); clusterValuesErr != nil {
			return fmt.Errorf("write helm values: %w", clusterValuesErr)
		}

		// Generate Terraform tfvars
		tfValues, err := config.ExportEnvironment(configDir, envName, "terraform")
		if err != nil {
			return fmt.Errorf("export terraform values: %w", err)
		}

		tfPath := filepath.Join(outputDir, "cluster.tfvars")
		if err := os.WriteFile(tfPath, []byte(tfValues), 0o644); err != nil {
			return fmt.Errorf("write terraform values: %w", err)
		}

		if !jsonOutput {
			fmt.Printf("Generated configuration for %s environment:\n", envName)
			fmt.Printf("  Helm values: %s\n", helmPath)
			fmt.Printf("  Terraform vars: %s\n", tfPath)
		} else {
			result := map[string]string{
				"environment": envName,
				"helmValues":  helmPath,
				"tfVars":      tfPath,
			}
			out, _ := json.Marshal(result)
			fmt.Println(string(out))
		}

		_ = env // Used for potential future expansion
		return nil
	},
}

var k8sKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig",
	Short: "Manage kubeconfig files",
	Long:  `Commands for managing kubeconfig files for different environments.`,
}

var k8sKubeconfigDecryptCmd = &cobra.Command{
	Use:   "decrypt [environment]",
	Short: "Decrypt kubeconfig for an environment",
	Long: `Decrypt the kubeconfig for the specified environment and make it available.

The decrypted kubeconfig is stored in .lab/kubeconfig/<env>.yaml`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		envName := "production"
		if len(args) > 0 {
			envName = args[0]
		}

		mgr := getKubeconfigManager()

		if err := mgr.SetupPersistent(envName); err != nil {
			return err
		}

		decPath := mgr.GetDecryptedPath(envName)
		if !jsonOutput {
			fmt.Printf("Kubeconfig decrypted for %s environment\n", envName)
			fmt.Printf("Path: %s\n", decPath)
			fmt.Printf("\nTo use:\n")
			fmt.Printf("  export KUBECONFIG=%s\n", decPath)
		} else {
			result := map[string]string{
				"environment": envName,
				"path":        decPath,
			}
			out, _ := json.Marshal(result)
			fmt.Println(string(out))
		}

		return nil
	},
}

var k8sKubeconfigCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove decrypted kubeconfig files",
	Long:  `Remove all decrypted kubeconfig files from the cache directory.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getKubeconfigManager()

		if err := mgr.CleanupAll(); err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}

		if !jsonOutput {
			fmt.Println("Decrypted kubeconfig files cleaned up")
		}

		return nil
	},
}

var k8sKubeconfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available kubeconfig files",
	Long:  `List all environments that have kubeconfig files available.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := getKubeconfigManager()

		envs, err := mgr.ListEnvironments()
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}

		if jsonOutput {
			out, _ := json.Marshal(envs)
			fmt.Println(string(out))
			return nil
		}

		if len(envs) == 0 {
			fmt.Println("No kubeconfig files found")
			fmt.Printf("Expected location: %s\n", filepath.Join(getConfigDir(), "kubeconfig", "<env>.enc.yaml"))
			return nil
		}

		fmt.Println("Available kubeconfigs:")
		for _, env := range envs {
			decPath := mgr.GetDecryptedPath(env)
			status := "encrypted"
			if _, err := os.Stat(decPath); err == nil {
				status = "decrypted"
			}
			fmt.Printf("  - %s (%s)\n", env, status)
		}

		return nil
	},
}

// parseK8sTarget parses a target string into tier and app
// e.g., "platform/gitea" -> ("platform", "gitea")
// e.g., "foundation" -> ("foundation", "")
// e.g., "gitea" -> ("platform", "gitea") // auto-detect
func parseK8sTarget(target string) (tier, app string) {
	if target == "" {
		return "", ""
	}

	parts := strings.SplitN(target, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	// Check if it's a tier name
	if parts[0] == "foundation" || parts[0] == "platform" || parts[0] == "apps" {
		return parts[0], ""
	}

	// Try to auto-detect tier by looking for the app
	for _, t := range []string{"foundation", "platform", "apps"} {
		appPath := filepath.Join("k8s", t, parts[0])
		if _, err := os.Stat(appPath); err == nil {
			return t, parts[0]
		}
	}

	// Default to assuming it's an app name
	return "", parts[0]
}

func diffTier(tier string) error {
	helmfilePath := filepath.Join("k8s", tier, "helmfile.yaml")
	if _, err := os.Stat(helmfilePath); err == nil {
		helmCmd := exec.Command("helmfile", "-f", helmfilePath, "diff")
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	// No helmfile, diff each app
	tierPath := filepath.Join("k8s", tier)
	entries, err := os.ReadDir(tierPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := diffApp(tier, entry.Name()); err != nil {
			fmt.Printf("Warning: %s/%s: %v\n", tier, entry.Name(), err)
		}
	}
	return nil
}

func diffApp(tier, app string) error {
	appPath := filepath.Join("k8s", tier, app)

	// Check for helmfile.yaml in app directory
	helmfilePath := filepath.Join(appPath, "helmfile.yaml")
	if _, err := os.Stat(helmfilePath); err == nil {
		if !jsonOutput {
			fmt.Printf("\n--- %s/%s ---\n", tier, app)
		}
		helmCmd := exec.Command("helmfile", "-f", helmfilePath, "diff")
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	// Check for Chart.yaml (local chart)
	chartPath := filepath.Join(appPath, "Chart.yaml")
	if _, err := os.Stat(chartPath); err == nil {
		if !jsonOutput {
			fmt.Printf("\n--- %s/%s ---\n", tier, app)
		}
		helmCmd := exec.Command("helm", "diff", "upgrade", app, appPath,
			"--namespace", app, "--allow-unreleased")
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	return nil
}

func syncTier(tier string, prune bool) error {
	helmfilePath := filepath.Join("k8s", tier, "helmfile.yaml")
	if _, err := os.Stat(helmfilePath); err == nil {
		args := []string{"-f", helmfilePath, "sync"}
		helmCmd := exec.Command("helmfile", args...)
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	return fmt.Errorf("no helmfile.yaml found for tier %s", tier)
}

func syncApp(tier, app string, prune bool) error {
	appPath := filepath.Join("k8s", tier, app)
	helmfilePath := filepath.Join(appPath, "helmfile.yaml")

	if _, err := os.Stat(helmfilePath); err == nil {
		if !jsonOutput {
			fmt.Printf("Syncing %s/%s via Helmfile...\n", tier, app)
		}
		helmCmd := exec.Command("helmfile", "-f", helmfilePath, "sync")
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	return fmt.Errorf("no helmfile.yaml found for %s/%s", tier, app)
}

func syncViaArgoCD(tier, app string, prune bool) error {
	var appName string
	if app != "" {
		appName = app
	} else if tier != "" {
		appName = tier
	} else {
		return fmt.Errorf("please specify an app or tier to sync")
	}

	if !jsonOutput {
		fmt.Printf("Syncing %s via ArgoCD...\n", appName)
	}

	args := []string{"app", "sync", appName, "--grpc-web"}
	if prune {
		args = append(args, "--prune")
	}

	argoCmd := exec.Command("argocd", args...)
	argoCmd.Stdout = os.Stdout
	argoCmd.Stderr = os.Stderr
	return argoCmd.Run()
}

func applyKustomization(path string, dryRun bool) error {
	kustomizePath := filepath.Join(path, "kustomization.yaml")
	if _, err := os.Stat(kustomizePath); err == nil {
		args := []string{"apply", "-k", path}
		if dryRun {
			args = append(args, "--dry-run=client")
		}
		kubectlCmd := exec.Command("kubectl", args...)
		kubectlCmd.Stdout = os.Stdout
		kubectlCmd.Stderr = os.Stderr
		return kubectlCmd.Run()
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.AddCommand(k8sCmd)

	// Add --env flag to k8s command for inheritance
	k8sCmd.PersistentFlags().String("env", "production", "Target environment (determines which kubeconfig to use)")

	k8sBootstrapCmd.Flags().Bool("dry-run", false, "Show what would be installed without making changes")
	k8sBootstrapCmd.Flags().Bool("skip-argocd", false, "Skip ArgoCD installation (for debugging)")
	k8sCmd.AddCommand(k8sBootstrapCmd)

	k8sCmd.AddCommand(k8sDiffCmd)

	k8sSyncCmd.Flags().Bool("argocd", false, "Use ArgoCD for sync instead of Helmfile")
	k8sSyncCmd.Flags().Bool("prune", false, "Prune resources not in the current configuration")
	k8sCmd.AddCommand(k8sSyncCmd)

	k8sCmd.AddCommand(k8sListCmd)

	k8sCmd.AddCommand(k8sStatusCmd)

	k8sGenerateCmd.Flags().String("output", "", "Output directory (default: config/gen)")
	k8sCmd.AddCommand(k8sGenerateCmd)

	// Kubeconfig subcommands
	k8sCmd.AddCommand(k8sKubeconfigCmd)
	k8sKubeconfigCmd.AddCommand(k8sKubeconfigDecryptCmd)
	k8sKubeconfigCmd.AddCommand(k8sKubeconfigCleanupCmd)
	k8sKubeconfigCmd.AddCommand(k8sKubeconfigListCmd)
}
