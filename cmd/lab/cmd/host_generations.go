package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/config"
)

func newHostGenerationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "generations <hostname>",
		Aliases: []string{"gen"},
		Short:   "Manage NixOS generations on a host",
		Long: `List or switch NixOS system generations on a remote host.

Without flags (or with --list), lists all available generations.
Use --switch <N> to activate a specific generation. By default this
activates it immediately and also sets it as the boot default. Add
--boot to only update the boot default without activating now.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]
			list, _ := cmd.Flags().GetBool("list")
			boot, _ := cmd.Flags().GetBool("boot")
			switchGen, _ := cmd.Flags().GetInt("switch")
			switchSet := cmd.Flags().Changed("switch")

			target := resolveHostTarget(hostname)

			if !switchSet || list {
				return runGenerationsList(cmd.Context(), hostname, target)
			}
			return runGenerationsSwitch(cmd.Context(), hostname, target, switchGen, boot)
		},
	}

	cmd.Flags().Bool("list", false, "List available generations (default behavior)")
	cmd.Flags().Int("switch", 0, "Switch to the specified generation number")
	cmd.Flags().Bool("boot", false, "Update boot default only; activate on next reboot instead of now")

	return cmd
}

func resolveHostTarget(hostname string) string {
	configDir := getConfigDir()
	env, err := config.LoadEnvironment(configDir, "production")
	if err != nil {
		return hostname
	}
	for _, host := range env.Hosts {
		if host.Name == hostname {
			return host.IP
		}
	}
	return hostname
}

type generation struct {
	Number  int    `json:"number"`
	Date    string `json:"date"`
	Current bool   `json:"current"`
}

func runGenerationsList(ctx context.Context, hostname, target string) error {
	// This needs to run as root so nix can create a lockfile for the system profile
	sshCmd := exec.CommandContext(ctx, "ssh", target,
		"sudo", "nix-env", "--list-generations", "--profile", "/nix/var/nix/profiles/system")
	sshCmd.Stderr = os.Stderr

	out, err := sshCmd.Output()
	if err != nil {
		return fmt.Errorf("list generations on %s: %w", hostname, err)
	}

	if !jsonOutput {
		fmt.Printf("Generations on %s:\n", hostname)
		fmt.Print(string(out))
		return nil
	}

	gens, err := parseGenerations(out)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(gens); err != nil {
		return fmt.Errorf("encoding generations: %w", err)
	}
	return nil
}

func parseGenerations(out []byte) ([]generation, error) {
	var gens []generation
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		num, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		date := fields[1] + " " + fields[2]
		current := len(fields) > 3 && strings.Contains(fields[3], "current")
		gens = append(gens, generation{Number: num, Date: date, Current: current})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning generations output: %w", err)
	}
	return gens, nil
}

func runGenerationsSwitch(ctx context.Context, hostname, target string, genNum int, bootOnly bool) error {
	if err := switchHostGeneration(ctx, hostname, target, genNum); err != nil {
		return err
	}

	if !bootOnly {
		if err := activateHostGeneration(ctx, hostname, target, genNum); err != nil {
			return err
		}
	}

	if err := setHostBootGeneration(ctx, hostname, target, genNum); err != nil {
		return err
	}

	return printGenerationSwitchResult(hostname, genNum, bootOnly)
}

// switchHostGeneration switches the active nix-env generation on target to genNum.
func switchHostGeneration(ctx context.Context, hostname, target string, genNum int) error {
	if !jsonOutput {
		fmt.Printf("Switching %s to generation %d...\n", hostname, genNum)
	}

	switchCmd := exec.CommandContext(ctx, "ssh", target,
		"sudo", "nix-env", "--switch-generation", strconv.Itoa(genNum),
		"--profile", "/nix/var/nix/profiles/system")
	switchCmd.Stdout = os.Stdout
	switchCmd.Stderr = os.Stderr
	if err := switchCmd.Run(); err != nil {
		return fmt.Errorf("switch generation on %s: %w", hostname, err)
	}
	return nil
}

// activateHostGeneration activates the switched-to generation on target immediately.
func activateHostGeneration(ctx context.Context, hostname, target string, genNum int) error {
	if !jsonOutput {
		fmt.Printf("Activating generation %d on %s...\n", genNum, hostname)
	}
	activateCmd := exec.CommandContext(ctx, "ssh", target,
		"sudo", "/nix/var/nix/profiles/system/bin/switch-to-configuration", "switch")
	activateCmd.Stdout = os.Stdout
	activateCmd.Stderr = os.Stderr
	if err := activateCmd.Run(); err != nil {
		return fmt.Errorf("activate generation on %s: %w", hostname, err)
	}
	return nil
}

// setHostBootGeneration sets the switched-to generation as the boot default on target.
func setHostBootGeneration(ctx context.Context, hostname, target string, genNum int) error {
	if !jsonOutput {
		fmt.Printf("Setting generation %d as boot default on %s...\n", genNum, hostname)
	}
	bootCmd := exec.CommandContext(ctx, "ssh", target,
		"sudo", "/nix/var/nix/profiles/system/bin/switch-to-configuration", "boot")
	bootCmd.Stdout = os.Stdout
	bootCmd.Stderr = os.Stderr
	if err := bootCmd.Run(); err != nil {
		return fmt.Errorf("set boot generation on %s: %w", hostname, err)
	}
	return nil
}

// printGenerationSwitchResult prints the outcome of a generation switch in the
// appropriate output format.
func printGenerationSwitchResult(hostname string, genNum int, bootOnly bool) error {
	if jsonOutput {
		result := map[string]any{
			"host":       hostname,
			"generation": genNum,
			"activated":  !bootOnly,
			"bootSet":    true,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("encoding result: %w", err)
		}
		return nil
	}

	if bootOnly {
		fmt.Printf("Generation %d will be activated on next boot of %s.\n", genNum, hostname)
	} else {
		fmt.Printf("Generation %d is now active and set as boot default on %s.\n", genNum, hostname)
	}
	return nil
}
