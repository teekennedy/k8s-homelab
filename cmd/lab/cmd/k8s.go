package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
	"github.com/teekennedy/homelab/cmd/lab/internal/helm"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
	"github.com/teekennedy/homelab/cmd/lab/kubeconfig"
)

var (
	kubeconfigMgr     *kubeconfig.Manager
	kubeconfigMgrOnce sync.Once
)

func getKubeconfigManager() *kubeconfig.Manager {
	kubeconfigMgrOnce.Do(func() {
		kubeconfigMgr = kubeconfig.NewManager(
			kubeconfig.WithConfigDir(paths.ProjectConfigDir()),
		)
	})
	return kubeconfigMgr
}

func setupKubeconfig(envName string) (func(), error) {
	mgr := getKubeconfigManager()

	if !mgr.Exists(envName) {
		return nil, fmt.Errorf("no kubeconfig found for environment %q (expected at %s)", envName, mgr.GetEncryptedPath(envName))
	}

	cleanup, err := mgr.Setup(envName)
	if err != nil {
		return nil, fmt.Errorf("setup kubeconfig: %w", err)
	}

	return cleanup, nil
}

func newK8sCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k8s",
		Short: "Kubernetes operations",
		Long: `Commands for managing Kubernetes resources and applications.

Kubeconfig files are stored encrypted per environment in config/kubeconfig/<env>.enc.yaml
and are automatically decrypted using sops when needed.`,
	}

	cmd.PersistentFlags().String("env", "production", "Target environment (determines which kubeconfig to use)")

	cmd.AddCommand(newK8sBootstrapCmd())
	cmd.AddCommand(newK8sDiffCmd())
	cmd.AddCommand(newK8sSyncCmd())
	cmd.AddCommand(newK8sListCmd())
	cmd.AddCommand(newK8sStatusCmd())
	cmd.AddCommand(newK8sGenerateCmd())
	cmd.AddCommand(newK8sKubeconfigCmd())

	return cmd
}

func newK8sBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
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

			bootstrapOrder := []string{
				"secret-system",
				"cert-system",
				"metallb",
			}

			if !skipArgo {
				bootstrapOrder = append(bootstrapOrder, "argocd")
			}

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

				chartPath := filepath.Join(appPath, "Chart.yaml")
				if _, err := os.Stat(chartPath); os.IsNotExist(err) {
					if err := applyKustomization(appPath, dryRun); err != nil {
						return fmt.Errorf("install %s: %w", app, err)
					}
					continue
				}

				needs, err := helm.NeedsDependencyBuild(appPath)
				if err != nil {
					return fmt.Errorf("check deps for %s: %w", app, err)
				}
				if needs {
					depCmd := exec.Command("helm", "dependency", "build", appPath)
					depCmd.Stdout = os.Stdout
					depCmd.Stderr = os.Stderr
					if err := depCmd.Run(); err != nil {
						return fmt.Errorf("build deps for %s: %w", app, err)
					}
				}

				helmArgs := []string{
					"upgrade", "--install", app, appPath,
					"--namespace", app,
					"--create-namespace",
				}
				if dryRun {
					helmArgs = append(helmArgs, "--dry-run")
				}

				clusterValues := filepath.Join(getConfigDir(), "gen", "cluster-values.yaml")
				if _, err := os.Stat(clusterValues); err == nil {
					helmArgs = append(helmArgs, "--values", clusterValues)
				}

				helmCmd := exec.Command("helm", helmArgs...)
				helmCmd.Stdout = os.Stdout
				helmCmd.Stderr = os.Stderr
				if err := helmCmd.Run(); err != nil {
					return fmt.Errorf("install %s: %w", app, err)
				}
			}

			if !skipArgo && !dryRun {
				if !jsonOutput {
					fmt.Println("\nApplying ArgoCD app-of-apps...")
				}

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

	cmd.Flags().Bool("dry-run", false, "Show what would be installed without making changes")
	cmd.Flags().Bool("skip-argocd", false, "Skip ArgoCD installation (for debugging)")

	return cmd
}

func newK8sDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff [app]",
		Short: "Show pending Kubernetes changes",
		Long: `Show what would change for Kubernetes resources.

Uses helm template piped through kubectl diff for semantic comparison.

Examples:
  lab k8s diff                    # Diff all tiers
  lab k8s diff foundation         # Diff entire foundation tier
  lab k8s diff platform/gitea     # Diff specific app
  lab k8s diff gitea              # Diff app (auto-detect tier)
  lab k8s diff --watch            # Watch for changes and re-diff
  lab k8s diff gitea --watch      # Watch specific app`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, _ := cmd.Flags().GetString("env")
			watch, _ := cmd.Flags().GetBool("watch")
			debounce, _ := cmd.Flags().GetDuration("debounce")

			cleanup, err := setupKubeconfig(envName)
			if err != nil {
				return err
			}
			defer cleanup()

			target := ""
			if len(args) > 0 {
				target = args[0]
			}

			if !watch {
				return runDiff(target)
			}

			return watchAndDiff(target, debounce)
		},
	}

	cmd.Flags().Bool("watch", false, "Watch for file changes and re-diff automatically")
	cmd.Flags().Duration("debounce", 50*time.Millisecond, "Debounce duration for watch mode")

	return cmd
}

func newK8sSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [app]",
		Short: "Sync Kubernetes resources",
		Long: `Force sync Kubernetes resources via Helm or ArgoCD.

Examples:
  lab k8s sync foundation         # Sync entire foundation tier
  lab k8s sync platform/gitea     # Sync specific app
  lab k8s sync gitea --argocd     # Sync via ArgoCD`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, _ := cmd.Flags().GetString("env")

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

	cmd.Flags().Bool("argocd", false, "Use ArgoCD for sync instead of Helmfile")
	cmd.Flags().Bool("prune", false, "Prune resources not in the current configuration")

	return cmd
}

func newK8sListCmd() *cobra.Command {
	return &cobra.Command{
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
}

func newK8sStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show cluster status",
		Long:  `Show the status of the Kubernetes cluster and deployed applications.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			envName, _ := cmd.Flags().GetString("env")

			cleanup, err := setupKubeconfig(envName)
			if err != nil {
				return err
			}
			defer cleanup()

			if !jsonOutput {
				fmt.Printf("Cluster Status (%s environment):\n", envName)
			}

			if !jsonOutput {
				fmt.Println("\nNodes:")
			}
			kubectlCmd := exec.Command("kubectl", "get", "nodes", "-o", "wide")
			kubectlCmd.Stdout = os.Stdout
			kubectlCmd.Stderr = os.Stderr
			if err := kubectlCmd.Run(); err != nil {
				return fmt.Errorf("get nodes: %w", err)
			}

			if !jsonOutput {
				fmt.Println("\nArgoCD Applications:")
			}
			argoCmd := exec.Command("argocd", "app", "list", "--grpc-web")
			argoCmd.Stdout = os.Stdout
			argoCmd.Stderr = os.Stderr
			if err := argoCmd.Run(); err != nil {
				kubectlCmd := exec.Command("kubectl", "get", "applications", "-n", "argocd",
					"-o", "custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status")
				kubectlCmd.Stdout = os.Stdout
				kubectlCmd.Stderr = os.Stderr
				kubectlCmd.Run()
			}

			return nil
		},
	}
}

func newK8sGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
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

			if outputDirErr := os.MkdirAll(outputDir, 0o755); outputDirErr != nil {
				return fmt.Errorf("create output directory: %w", outputDirErr)
			}

			helmValues, err := config.ExportEnvironment(configDir, envName, "helm")
			if err != nil {
				return fmt.Errorf("export helm values: %w", err)
			}

			helmPath := filepath.Join(outputDir, "cluster-values.yaml")
			if clusterValuesErr := os.WriteFile(helmPath, []byte(helmValues), 0o644); clusterValuesErr != nil {
				return fmt.Errorf("write helm values: %w", clusterValuesErr)
			}

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

			_ = env
			return nil
		},
	}

	cmd.Flags().String("output", "", "Output directory (default: config/gen)")

	return cmd
}

func newK8sKubeconfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Manage kubeconfig files",
		Long:  `Commands for managing kubeconfig files for different environments.`,
	}

	cmd.AddCommand(newK8sKubeconfigDecryptCmd())
	cmd.AddCommand(newK8sKubeconfigCleanupCmd())
	cmd.AddCommand(newK8sKubeconfigListCmd())

	return cmd
}

func newK8sKubeconfigDecryptCmd() *cobra.Command {
	return &cobra.Command{
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
}

func newK8sKubeconfigCleanupCmd() *cobra.Command {
	return &cobra.Command{
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
}

func newK8sKubeconfigListCmd() *cobra.Command {
	return &cobra.Command{
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
}

func parseK8sTarget(target string) (tier, app string) {
	if target == "" {
		return "", ""
	}

	parts := strings.SplitN(target, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	if parts[0] == "foundation" || parts[0] == "platform" || parts[0] == "apps" {
		return parts[0], ""
	}

	for _, t := range []string{"foundation", "platform", "apps"} {
		appPath := filepath.Join("k8s", t, parts[0])
		if _, err := os.Stat(appPath); err == nil {
			return t, parts[0]
		}
	}

	return "", parts[0]
}

func runDiff(target string) error {
	tier, app := parseK8sTarget(target)

	if tier == "" && app == "" {
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
		return diffTier(tier)
	}

	return diffApp(tier, app)
}

func diffTier(tier string) error {
	tierPath := filepath.Join("k8s", tier)
	charts, err := helm.DiscoverCharts(tierPath)
	if err != nil {
		return err
	}

	for _, chart := range charts {
		if err := diffApp(chart.Tier, chart.Name); err != nil {
			fmt.Printf("Warning: %s/%s: %v\n", chart.Tier, chart.Name, err)
		}
	}
	return nil
}

func diffApp(tier, app string) error {
	chartDir := filepath.Join("k8s", tier, app)

	info, err := helm.ParseChartInfo(chartDir)
	if err != nil {
		return fmt.Errorf("parse chart info: %w", err)
	}

	needs, err := helm.NeedsDependencyBuild(chartDir)
	if err != nil {
		return err
	}
	if needs {
		if !jsonOutput {
			fmt.Printf("Building dependencies for %s/%s...\n", tier, app)
		}
		depCmd := exec.Command("helm", "dependency", "build", chartDir)
		depCmd.Stdout = os.Stdout
		depCmd.Stderr = os.Stderr
		if err := depCmd.Run(); err != nil {
			return fmt.Errorf("helm dependency build: %w", err)
		}
	}

	if !jsonOutput {
		fmt.Printf("\n--- %s/%s ---\n", tier, app)
	}

	templateArgs := []string{
		"template", info.ReleaseName, chartDir,
		"--namespace", info.Namespace,
	}

	clusterValues := filepath.Join(getConfigDir(), "gen", "cluster-values.yaml")
	if _, err := os.Stat(clusterValues); err == nil {
		templateArgs = append(templateArgs, "--values", clusterValues)
	}

	helmCmd := exec.Command("helm", templateArgs...)
	kubectlCmd := exec.Command("kubectl", "diff", "-f", "-")

	pipe, err := helmCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	kubectlCmd.Stdin = pipe
	kubectlCmd.Stdout = os.Stdout
	kubectlCmd.Stderr = os.Stderr

	if err := kubectlCmd.Start(); err != nil {
		helmCmd.Stdout = os.Stdout
		helmCmd.Stderr = os.Stderr
		return helmCmd.Run()
	}

	if err := helmCmd.Run(); err != nil {
		kubectlCmd.Wait()
		return fmt.Errorf("helm template: %w", err)
	}

	if err := kubectlCmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("kubectl diff: %w", err)
	}

	if !jsonOutput {
		fmt.Println("  (no changes)")
	}
	return nil
}

func watchAndDiff(target string, debounce time.Duration) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	tier, app := parseK8sTarget(target)
	var watchPaths []string
	if tier != "" && app != "" {
		watchPaths = []string{filepath.Join("k8s", tier, app)}
	} else if tier != "" {
		watchPaths = []string{filepath.Join("k8s", tier)}
	} else {
		watchPaths = []string{"k8s"}
	}

	for _, wp := range watchPaths {
		filepath.WalkDir(wp, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "charts" || name == ".venv" || name == "__pycache__" || name == ".pytest_cache" {
					return filepath.SkipDir
				}
				watcher.Add(path)
			}
			return nil
		})
	}

	fmt.Println("Watching for changes... (Ctrl+C to stop)")
	runDiff(target)

	var timer *time.Timer

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}

			ext := filepath.Ext(event.Name)
			if ext != ".yaml" && ext != ".yml" && ext != ".tpl" {
				continue
			}

			if timer != nil {
				timer.Stop()
			}

			changedFile := event.Name
			timer = time.AfterFunc(debounce, func() {
				fmt.Printf("\n--- File changed: %s ---\n", changedFile)

				chartDir := findChartDir(changedFile)
				if chartDir == "" {
					runDiff(target)
					return
				}

				info, err := helm.ParseChartInfo(chartDir)
				if err != nil {
					fmt.Printf("Error: %v\n", err)
					return
				}

				diffApp(info.Tier, info.Name)
				fmt.Println("\nWatching for changes... (Ctrl+C to stop)")
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Printf("Watch error: %v\n", err)
		}
	}
}

func findChartDir(filePath string) string {
	dir := filepath.Dir(filePath)
	for dir != "." && dir != "k8s" {
		if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func syncTier(tier string, prune bool) error {
	tierPath := filepath.Join("k8s", tier)
	charts, err := helm.DiscoverCharts(tierPath)
	if err != nil {
		return err
	}

	for _, chart := range charts {
		if err := syncApp(chart.Tier, chart.Name, prune); err != nil {
			fmt.Printf("Warning: %s/%s: %v\n", chart.Tier, chart.Name, err)
		}
	}
	return nil
}

func syncApp(tier, app string, prune bool) error {
	chartDir := filepath.Join("k8s", tier, app)

	info, err := helm.ParseChartInfo(chartDir)
	if err != nil {
		return fmt.Errorf("parse chart info: %w", err)
	}

	needs, err := helm.NeedsDependencyBuild(chartDir)
	if err != nil {
		return err
	}
	if needs {
		if !jsonOutput {
			fmt.Printf("Building dependencies for %s/%s...\n", tier, app)
		}
		depCmd := exec.Command("helm", "dependency", "build", chartDir)
		depCmd.Stdout = os.Stdout
		depCmd.Stderr = os.Stderr
		if err := depCmd.Run(); err != nil {
			return fmt.Errorf("helm dependency build: %w", err)
		}
	}

	if !jsonOutput {
		fmt.Printf("Syncing %s/%s via Helm...\n", tier, app)
	}

	upgradeArgs := []string{
		"upgrade", "--install", info.ReleaseName, chartDir,
		"--namespace", info.Namespace,
		"--create-namespace",
	}

	clusterValues := filepath.Join(getConfigDir(), "gen", "cluster-values.yaml")
	if _, err := os.Stat(clusterValues); err == nil {
		upgradeArgs = append(upgradeArgs, "--values", clusterValues)
	}

	helmCmd := exec.Command("helm", upgradeArgs...)
	helmCmd.Stdout = os.Stdout
	helmCmd.Stderr = os.Stderr
	return helmCmd.Run()
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
