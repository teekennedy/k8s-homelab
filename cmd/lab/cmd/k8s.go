package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var k8sCmd = &cobra.Command{
	Use:   "k8s",
	Short: "Kubernetes operations",
	Long:  `Commands for managing Kubernetes resources and applications.`,
}

var k8sBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap Kubernetes cluster",
	Long:  `Bootstrap the Kubernetes cluster with foundation components using Helmfile.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		env, _ := cmd.Flags().GetString("env")
		fmt.Printf("Bootstrapping Kubernetes cluster (env=%s)...\n", env)

		// Run helmfile apply for foundation
		helmfileCmd := exec.Command("helmfile", "-f", "k8s/foundation/helmfile.yaml", "apply")
		helmfileCmd.Stdout = os.Stdout
		helmfileCmd.Stderr = os.Stderr
		return helmfileCmd.Run()
	},
}

var k8sDiffCmd = &cobra.Command{
	Use:   "diff [path]",
	Short: "Show pending Kubernetes changes",
	Long: `Show what would change for Kubernetes resources.
Path can be a tier (foundation, platform, apps) or a specific app (platform/gitea).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		fmt.Printf("Showing diff for %s...\n", path)

		// Run helm diff via helmfile
		helmfileCmd := exec.Command("helmfile", "-f", fmt.Sprintf("k8s/%s/helmfile.yaml", path), "diff")
		helmfileCmd.Stdout = os.Stdout
		helmfileCmd.Stderr = os.Stderr
		return helmfileCmd.Run()
	},
}

var k8sSyncCmd = &cobra.Command{
	Use:   "sync [path]",
	Short: "Sync Kubernetes resources",
	Long: `Force sync Kubernetes resources via ArgoCD or Helmfile.
Path can be a tier (foundation, platform, apps) or a specific app (platform/gitea).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}

		useArgo, _ := cmd.Flags().GetBool("argocd")

		if useArgo {
			fmt.Printf("Syncing %s via ArgoCD...\n", path)
			argoCmd := exec.Command("argocd", "app", "sync", path)
			argoCmd.Stdout = os.Stdout
			argoCmd.Stderr = os.Stderr
			return argoCmd.Run()
		}

		fmt.Printf("Syncing %s via Helmfile...\n", path)
		helmfileCmd := exec.Command("helmfile", "-f", fmt.Sprintf("k8s/%s/helmfile.yaml", path), "sync")
		helmfileCmd.Stdout = os.Stdout
		helmfileCmd.Stderr = os.Stderr
		return helmfileCmd.Run()
	},
}

var k8sStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cluster status",
	Long:  `Show the status of the Kubernetes cluster and deployed applications.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Cluster Status:")

		// Get node status
		fmt.Println("\nNodes:")
		kubectlCmd := exec.Command("kubectl", "get", "nodes", "-o", "wide")
		kubectlCmd.Stdout = os.Stdout
		kubectlCmd.Stderr = os.Stderr
		if err := kubectlCmd.Run(); err != nil {
			return err
		}

		// Get ArgoCD app status
		fmt.Println("\nArgoCD Applications:")
		argoCmd := exec.Command("argocd", "app", "list")
		argoCmd.Stdout = os.Stdout
		argoCmd.Stderr = os.Stderr
		argoCmd.Run() // Don't fail if argocd not available

		return nil
	},
}

func init() {
	rootCmd.AddCommand(k8sCmd)

	k8sBootstrapCmd.Flags().String("env", "production", "Target environment")
	k8sCmd.AddCommand(k8sBootstrapCmd)

	k8sCmd.AddCommand(k8sDiffCmd)

	k8sSyncCmd.Flags().Bool("argocd", false, "Use ArgoCD for sync instead of Helmfile")
	k8sCmd.AddCommand(k8sSyncCmd)

	k8sCmd.AddCommand(k8sStatusCmd)
}
