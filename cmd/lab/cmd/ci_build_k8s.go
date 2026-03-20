package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/internal/helm"
)

func newCIBuildK8sCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k8s [paths...]",
		Short: "Render Helm templates for Kubernetes charts",
		Long: `Render Helm templates for charts under the k8s/ directory.

This downloads chart dependencies if needed and runs helm template to verify
that all charts render successfully.

Examples:
  lab ci build k8s                           # Render all charts
  lab ci build k8s k8s/apps/terraria         # Render specific chart
  lab ci build k8s k8s/foundation            # Render all foundation charts
  lab ci build k8s k8s/apps k8s/platform     # Render multiple directories
  lab ci build k8s --changed                 # Render only changed charts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			changed, _ := cmd.Flags().GetBool("changed")
			cmd.SilenceUsage = true

			paths := args
			if changed && len(paths) == 0 {
				changedPaths, err := helm.ChangedChartPaths("")
				if err != nil {
					return fmt.Errorf("detect changed charts: %w", err)
				}
				if len(changedPaths) == 0 {
					fmt.Println("No changed charts detected")
					return nil
				}
				paths = changedPaths
				if verbose {
					fmt.Printf("Changed charts: %s\n", strings.Join(paths, ", "))
				}
			}

			daggerArgs := []string{"call", "build-helm", "--source=."}
			for _, p := range paths {
				daggerArgs = append(daggerArgs, "--paths", p)
			}

			return runDaggerInCI(daggerArgs...)
		},
	}
	cmd.Flags().Bool("changed", false, "Only render charts with changes (detected via git diff)")
	return cmd
}
