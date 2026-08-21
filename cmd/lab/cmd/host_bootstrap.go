package cmd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/teekennedy/homelab/cmd/lab/internal/paths"
	"gopkg.in/yaml.v3"
)

type bootstrapper struct {
	hostname       string
	ip             string
	repoRoot       string
	hostDir        string
	secretsPath    string
	sopsConfigPath string
	facterPath     string

	sshPubKey string
	tempDir   string
}

func newHostBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap <hostname>",
		Short: "Bootstrap a new NixOS host",
		Long: `Fully bootstrap a new NixOS host, including:

1. Validating the host is defined in flake.nix
2. Creating the host config directory
3. Generating SSH keys and user password (with sops encryption)
4. Updating .sops.yaml with the host's age key and creation rules
5. Generating hardware configuration via nixos-facter
6. Verifying disko disk configuration
7. Building and verifying the NixOS configuration
8. Installing NixOS via nixos-anywhere

Each step is idempotent - the command can be re-run safely without
losing progress or redoing completed work.

Prerequisites:
  - Host must be defined in flake.nix (in borgHosts list)
  - Target must be booted from the NixOS installer ISO
  - SSH access to root@<ip> must work`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			hostname := args[0]
			ip, _ := cmd.Flags().GetString("ip")

			b := &bootstrapper{
				hostname: hostname,
				ip:       ip,
			}
			return b.run(cmd.Context())
		},
	}

	cmd.Flags().String("ip", "", "IP address of the target host (booted from installer)")
	_ = cmd.MarkFlagRequired("ip")

	return cmd
}

func (b *bootstrapper) logStep(format string, a ...any) {
	fmt.Printf("\n==> "+format+"\n", a...)
}

func (b *bootstrapper) run(ctx context.Context) error {
	var err error
	b.repoRoot, err = paths.RepoRoot()
	if err != nil {
		return fmt.Errorf("find repo root: %w", err)
	}
	b.hostDir = filepath.Join(b.repoRoot, "nix", "hosts", b.hostname)
	b.secretsPath = filepath.Join(b.hostDir, "secrets.yaml")
	b.sopsConfigPath = filepath.Join(b.repoRoot, ".sops.yaml")
	b.facterPath = filepath.Join(b.hostDir, "facter.json")

	b.tempDir, err = os.MkdirTemp("", "lab-bootstrap-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(b.tempDir) }()

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"Validating host in flake.nix", b.validateFlakeHost},
		{"Creating host directory", b.createHostDir},
		{"Ensuring host secrets", b.ensureSecrets},
		{"Updating .sops.yaml age key", b.updateSopsAgeKey},
		{"Adding host creation rule to .sops.yaml", b.ensureHostCreationRule},
		{"Encrypting secrets", b.encryptSecrets},
		{"Updating modules creation rule", b.updateModulesCreationRule},
		{"Generating hardware configuration", b.generateFacterConfig},
		{"Checking disko configuration", b.checkDiskoConfig},
		{"Building and verifying NixOS configuration", b.buildAndVerify},
		{"Installing NixOS", b.install},
	}

	for _, step := range steps {
		b.logStep(step.name)
		if err := step.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}

	fmt.Printf("\nBootstrap complete! Host %s should be rebooting into NixOS.\n", b.hostname)
	fmt.Printf("Once it's up, run: lab host deploy %s\n", b.hostname)
	return nil
}

func (b *bootstrapper) validateFlakeHost(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "nix", "eval",
		".#nixosConfigurations",
		"--apply", fmt.Sprintf(`x: builtins.hasAttr "%s" x`, b.hostname),
		"--json")
	cmd.Dir = b.repoRoot
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to evaluate flake: %w\nMake sure the host is defined in flake.nix", err)
	}
	if strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("host %q not found in flake.nix nixosConfigurations\nAdd a definition for %s to the borgHosts list in flake.nix before bootstrapping", b.hostname, b.hostname)
	}
	fmt.Printf("Host %s found in flake.nix\n", b.hostname)
	return nil
}

func (b *bootstrapper) createHostDir(ctx context.Context) error {
	if info, err := os.Stat(b.hostDir); err == nil && info.IsDir() {
		fmt.Printf("Host directory already exists: %s\n", b.hostDir)
	} else {
		if err := os.MkdirAll(b.hostDir, 0o750); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
		fmt.Printf("Created %s\n", b.hostDir)
	}

	// Ensure default.nix exists (required by flake.nix's module import)
	defaultNix := filepath.Join(b.hostDir, "default.nix")
	if _, err := os.Stat(defaultNix); err != nil {
		if err := os.WriteFile(defaultNix, []byte("{...}: {}\n"), 0o600); err != nil {
			return fmt.Errorf("create default.nix: %w", err)
		}
		fmt.Println("Created default.nix")
		return b.gitAdd(ctx, defaultNix)
	}
	fmt.Println("default.nix already exists")
	return nil
}

func (b *bootstrapper) ensureSecrets(ctx context.Context) error {
	existingSecrets, isEncrypted, err := b.loadExistingSecrets(ctx)
	if err != nil {
		return err
	}

	needsUpdate, err := b.ensureSecretFields(ctx, existingSecrets)
	if err != nil {
		return err
	}

	b.sshPubKey = existingSecrets["ssh_host_public_key"]

	if !needsUpdate {
		if isEncrypted {
			fmt.Println("All secret fields present, no changes needed")
		}
		return nil
	}

	return b.writePlaintextSecrets(existingSecrets)
}

// ensureSecretFields ensures the SSH keys, password hash, and restic password
// fields are all populated in existingSecrets, generating any that are missing.
// Returns true if any field was newly generated.
func (b *bootstrapper) ensureSecretFields(ctx context.Context, existingSecrets map[string]string) (bool, error) {
	sshUpdated, err := b.ensureSSHKeys(ctx, existingSecrets)
	if err != nil {
		return false, err
	}
	passwordUpdated, err := b.ensurePasswordHash(ctx, existingSecrets)
	if err != nil {
		return false, err
	}
	resticUpdated, err := ensureResticPassword(existingSecrets)
	if err != nil {
		return false, err
	}
	return sshUpdated || passwordUpdated || resticUpdated, nil
}

// writePlaintextSecrets writes secrets to secretsPath in plaintext, to be
// encrypted by a later bootstrap step.
func (b *bootstrapper) writePlaintextSecrets(secrets map[string]string) error {
	data, err := yaml.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}
	if err := os.WriteFile(b.secretsPath, data, 0o600); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	fmt.Println("Wrote secrets.yaml (plaintext, will encrypt after .sops.yaml setup)")
	return nil
}

// loadExistingSecrets loads secrets.yaml if present, trying sops decryption first
// and falling back to plaintext (e.g. from a previous partial run).
func (b *bootstrapper) loadExistingSecrets(ctx context.Context) (map[string]string, bool, error) {
	existingSecrets := make(map[string]string)

	if _, err := os.Stat(b.secretsPath); err != nil {
		return existingSecrets, false, nil
	}

	// File exists - try decrypting first (might be encrypted from previous run)
	decryptCmd := exec.CommandContext(ctx, "sops", "decrypt", b.secretsPath)
	decryptCmd.Dir = b.repoRoot
	output, decryptErr := decryptCmd.Output()
	if decryptErr == nil {
		if err := yaml.Unmarshal(output, &existingSecrets); err != nil {
			return nil, false, fmt.Errorf("parse decrypted secrets: %w", err)
		}
		return existingSecrets, true, nil
	}

	// Maybe plaintext from a previous partial run
	data, err := os.ReadFile(b.secretsPath)
	if err != nil {
		return nil, false, fmt.Errorf("read secrets.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, &existingSecrets); err != nil {
		return nil, false, fmt.Errorf("secrets.yaml exists but cannot be parsed (encrypted without matching sops rule, or invalid YAML): %w", err)
	}
	return existingSecrets, false, nil
}

// ensureSSHKeys generates an SSH host key pair into existingSecrets if not already present.
// Returns true if a new key pair was generated.
func (b *bootstrapper) ensureSSHKeys(ctx context.Context, existingSecrets map[string]string) (bool, error) {
	if existingSecrets["ssh_host_public_key"] != "" && existingSecrets["ssh_host_private_key"] != "" {
		fmt.Println("SSH keys already present")
		return false, nil
	}

	fmt.Println("Generating SSH host key pair...")
	keyPath := filepath.Join(b.tempDir, "ssh_host_ed25519_key")
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-t", "ed25519", "-C", b.hostname, "-N", "", "-f", keyPath)
	if verbose {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("ssh-keygen: %w", err)
	}

	pubKey, err := os.ReadFile(keyPath + ".pub") //nolint:gosec // keyPath is generated internally, not user input
	if err != nil {
		return false, fmt.Errorf("read public key: %w", err)
	}
	privKey, err := os.ReadFile(keyPath) //nolint:gosec // keyPath is generated internally, not user input
	if err != nil {
		return false, fmt.Errorf("read private key: %w", err)
	}
	existingSecrets["ssh_host_public_key"] = strings.TrimSpace(string(pubKey))
	existingSecrets["ssh_host_private_key"] = string(privKey)
	return true, nil
}

// ensurePasswordHash prompts for and hashes the default user's password into
// existingSecrets if not already present. Returns true if a new hash was generated.
func (b *bootstrapper) ensurePasswordHash(ctx context.Context, existingSecrets map[string]string) (bool, error) {
	if existingSecrets["default_user_hashed_password"] != "" {
		fmt.Println("Password hash already present")
		return false, nil
	}

	password, err := readPassword(ctx, fmt.Sprintf("Password for %s default user: ", b.hostname))
	if err != nil {
		return false, err
	}
	if password == "" {
		return false, fmt.Errorf("password cannot be empty")
	}

	hashCmd := exec.CommandContext(ctx, "mkpasswd", "--method=SHA-512", password)
	hashOutput, err := hashCmd.Output()
	if err != nil {
		return false, fmt.Errorf("mkpasswd: %w", err)
	}
	existingSecrets["default_user_hashed_password"] = strings.TrimSpace(string(hashOutput))
	return true, nil
}

// ensureResticPassword generates a restic repo password into existingSecrets if not
// already present. Returns true if a new password was generated.
func ensureResticPassword(existingSecrets map[string]string) (bool, error) {
	if existingSecrets["restic_repo_password"] != "" {
		fmt.Println("Restic repo password already present")
		return false, nil
	}

	password, err := generateSecurePassword(24)
	if err != nil {
		return false, fmt.Errorf("generate restic password: %w", err)
	}
	existingSecrets["restic_repo_password"] = password
	fmt.Println("Generated restic_repo_password")
	return true, nil
}

func (b *bootstrapper) updateSopsAgeKey(ctx context.Context) error {
	// Convert SSH public key to age key
	cmd := exec.CommandContext(ctx, "ssh-to-age")
	cmd.Stdin = strings.NewReader(b.sshPubKey + "\n")
	ageOutput, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ssh-to-age: %w", err)
	}
	ageKey := strings.TrimSpace(string(ageOutput))

	doc, err := b.loadSopsConfig()
	if err != nil {
		return err
	}
	root := rootMapping(doc)
	keys := findMapValue(root, "keys")
	if keys == nil || keys.Kind != yaml.SequenceNode {
		return fmt.Errorf("'keys' not found or not a sequence in .sops.yaml")
	}

	anchor := "host_" + b.hostname

	// Check if key with this anchor already exists
	for _, node := range keys.Content {
		if node.Anchor == anchor {
			if node.Value == ageKey {
				fmt.Printf("Age key for %s already present and up to date\n", b.hostname)
				return nil
			}
			node.Value = ageKey
			fmt.Printf("Updated age key for %s\n", b.hostname)
			return b.saveSopsConfig(doc)
		}
	}

	// Add new key with anchor
	newNode := &yaml.Node{
		Kind:   yaml.ScalarNode,
		Value:  ageKey,
		Anchor: anchor,
	}
	keys.Content = append(keys.Content, newNode)
	fmt.Printf("Added age key for %s (anchor: &%s)\n", b.hostname, anchor)
	return b.saveSopsConfig(doc)
}

func (b *bootstrapper) ensureHostCreationRule(context.Context) error {
	doc, err := b.loadSopsConfig()
	if err != nil {
		return err
	}
	root := rootMapping(doc)
	rules := findMapValue(root, "creation_rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return fmt.Errorf("'creation_rules' not found in .sops.yaml")
	}

	expectedRegex := fmt.Sprintf(`nix/hosts/%s/secrets\.yaml`, b.hostname)
	if creationRuleExists(rules, expectedRegex) {
		fmt.Println("Creation rule already exists")
		return nil
	}

	// Find common keys (user PGP + personal age) from an existing host rule
	commonPGP, commonAge, err := b.findCommonKeys(root)
	if err != nil {
		return fmt.Errorf("find common keys: %w", err)
	}

	keys := findMapValue(root, "keys")
	hostAnchor := "host_" + b.hostname
	hostKeyNode := findKeyByAnchor(keys, hostAnchor)
	if hostKeyNode == nil {
		return fmt.Errorf("host age key &%s not found in .sops.yaml keys", hostAnchor)
	}

	newRule := buildHostCreationRule(expectedRegex, commonPGP, commonAge, hostKeyNode)
	insertHostRule(rules, newRule)

	fmt.Printf("Added creation rule for %s\n", expectedRegex)
	return b.saveSopsConfig(doc)
}

// findKeyByAnchor returns the child of keys with the given YAML anchor, or nil if not found.
func findKeyByAnchor(keys *yaml.Node, anchor string) *yaml.Node {
	for _, n := range keys.Content {
		if n.Anchor == anchor {
			return n
		}
	}
	return nil
}

// creationRuleExists reports whether rules already contains a rule with the given path_regex.
func creationRuleExists(rules *yaml.Node, expectedRegex string) bool {
	for _, rule := range rules.Content {
		pr := findMapValue(rule, "path_regex")
		if pr != nil && pr.Value == expectedRegex {
			return true
		}
	}
	return false
}

// buildHostCreationRule builds a new sops creation_rules entry for expectedRegex,
// granting access to the common PGP/age keys plus the host's own age key.
func buildHostCreationRule(expectedRegex string, commonPGP, commonAge []*yaml.Node, hostKeyNode *yaml.Node) *yaml.Node {
	pgpSeq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, k := range commonPGP {
		pgpSeq.Content = append(pgpSeq.Content, &yaml.Node{Kind: yaml.AliasNode, Value: k.Anchor, Alias: k})
	}

	ageSeq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, k := range commonAge {
		ageSeq.Content = append(ageSeq.Content, &yaml.Node{Kind: yaml.AliasNode, Value: k.Anchor, Alias: k})
	}
	ageSeq.Content = append(ageSeq.Content, &yaml.Node{Kind: yaml.AliasNode, Value: hostKeyNode.Anchor, Alias: hostKeyNode})

	keyGroup := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "pgp"},
			pgpSeq,
			{Kind: yaml.ScalarNode, Value: "age"},
			ageSeq,
		},
	}

	return &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "path_regex"},
			{Kind: yaml.ScalarNode, Value: expectedRegex},
			{Kind: yaml.ScalarNode, Value: "key_groups"},
			{Kind: yaml.SequenceNode, Content: []*yaml.Node{keyGroup}},
		},
	}
}

// insertHostRule inserts newRule into rules immediately after the last existing
// nix/hosts/ rule (keeping host rules grouped ahead of modules/terraform rules).
func insertHostRule(rules *yaml.Node, newRule *yaml.Node) {
	insertIdx := 0
	for i, rule := range rules.Content {
		pr := findMapValue(rule, "path_regex")
		if pr != nil && strings.HasPrefix(pr.Value, "nix/hosts/") {
			insertIdx = i + 1
		}
	}

	content := make([]*yaml.Node, 0, len(rules.Content)+1)
	content = append(content, rules.Content[:insertIdx]...)
	content = append(content, newRule)
	content = append(content, rules.Content[insertIdx:]...)
	rules.Content = content
}

func (b *bootstrapper) encryptSecrets(ctx context.Context) error {
	data, err := os.ReadFile(b.secretsPath)
	if err != nil {
		return fmt.Errorf("read secrets.yaml: %w", err)
	}

	var contents map[string]any
	if err := yaml.Unmarshal(data, &contents); err != nil {
		return fmt.Errorf("parse secrets.yaml: %w", err)
	}

	// sops-encrypted files contain a "sops" metadata key
	if _, hasSops := contents["sops"]; hasSops {
		fmt.Println("secrets.yaml is already encrypted")
		return b.gitAdd(ctx, b.secretsPath)
	}

	cmd := exec.CommandContext(ctx, "sops", "encrypt", "--in-place", b.secretsPath)
	cmd.Dir = b.repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sops encrypt: %w", err)
	}

	fmt.Println("Encrypted secrets.yaml")
	return b.gitAdd(ctx, b.secretsPath, b.sopsConfigPath)
}

func (b *bootstrapper) updateModulesCreationRule(ctx context.Context) error {
	doc, err := b.loadSopsConfig()
	if err != nil {
		return err
	}
	root := rootMapping(doc)
	rules := findMapValue(root, "creation_rules")
	keys := findMapValue(root, "keys")

	modulesRule := findRuleByPathSubstring(rules, "nix/modules")
	if modulesRule == nil {
		return fmt.Errorf("no creation rule found for nix/modules in .sops.yaml")
	}

	hostAnchor := "host_" + b.hostname
	hostKeyNode := findKeyByAnchor(keys, hostAnchor)
	if hostKeyNode == nil {
		return fmt.Errorf("host key &%s not found in .sops.yaml", hostAnchor)
	}

	ageSeq, err := modulesRuleAgeSeq(modulesRule)
	if err != nil {
		return err
	}

	if ageSeqHasAnchor(ageSeq, hostAnchor) {
		fmt.Println("Host key already in modules creation rule")
		return nil
	}

	ageSeq.Content = append(ageSeq.Content, &yaml.Node{
		Kind:  yaml.AliasNode,
		Value: hostKeyNode.Anchor,
		Alias: hostKeyNode,
	})

	if err := b.saveSopsConfig(doc); err != nil {
		return err
	}
	fmt.Println("Added host key to modules creation rule")

	moduleFiles, err := filepath.Glob(filepath.Join(b.repoRoot, "nix", "modules", "*", "*.enc.yaml"))
	if err != nil {
		return fmt.Errorf("globbing module encrypted files: %w", err)
	}

	if err := b.reEncryptFiles(ctx, moduleFiles); err != nil {
		return err
	}

	// Stage all modified files
	gitPaths := []string{b.sopsConfigPath}
	gitPaths = append(gitPaths, moduleFiles...)
	return b.gitAdd(ctx, gitPaths...)
}

// findRuleByPathSubstring returns the first rule in rules whose path_regex contains substr.
func findRuleByPathSubstring(rules *yaml.Node, substr string) *yaml.Node {
	for _, rule := range rules.Content {
		pr := findMapValue(rule, "path_regex")
		if pr != nil && strings.Contains(pr.Value, substr) {
			return rule
		}
	}
	return nil
}

// modulesRuleAgeSeq returns the "age" key sequence from a creation rule's first key group.
func modulesRuleAgeSeq(rule *yaml.Node) (*yaml.Node, error) {
	keyGroups := findMapValue(rule, "key_groups")
	if keyGroups == nil || len(keyGroups.Content) == 0 {
		return nil, fmt.Errorf("no key_groups in modules creation rule")
	}
	ageSeq := findMapValue(keyGroups.Content[0], "age")
	if ageSeq == nil {
		return nil, fmt.Errorf("no 'age' key group in modules creation rule")
	}
	return ageSeq, nil
}

// ageSeqHasAnchor reports whether ageSeq already contains a key (or alias to a key)
// with the given anchor.
func ageSeqHasAnchor(ageSeq *yaml.Node, anchor string) bool {
	for _, k := range ageSeq.Content {
		resolved := k
		if k.Kind == yaml.AliasNode {
			resolved = k.Alias
		}
		if resolved.Anchor == anchor {
			return true
		}
	}
	return false
}

// reEncryptFiles runs `sops updatekeys` on each file to bring it in line with
// the current .sops.yaml creation rules.
func (b *bootstrapper) reEncryptFiles(ctx context.Context, files []string) error {
	for _, f := range files {
		rel, _ := filepath.Rel(b.repoRoot, f)
		fmt.Printf("Re-encrypting %s\n", rel)
		reEncryptCmd := exec.CommandContext(ctx, "sops", "updatekeys", "-y", f)
		reEncryptCmd.Dir = b.repoRoot
		reEncryptCmd.Stdout = os.Stdout
		reEncryptCmd.Stderr = os.Stderr
		if err := reEncryptCmd.Run(); err != nil {
			return fmt.Errorf("re-encrypt %s: %w", rel, err)
		}
	}
	return nil
}

func (b *bootstrapper) generateFacterConfig(ctx context.Context) error {
	if _, err := os.Stat(b.facterPath); err == nil {
		fmt.Println("facter.json already exists, skipping hardware config generation")
		return b.gitAdd(ctx, b.facterPath)
	}

	facterRelPath := fmt.Sprintf("nix/hosts/%s/facter.json", b.hostname)
	cmd := exec.CommandContext(ctx, "nixos-anywhere",
		"--flake", fmt.Sprintf(".#%s", b.hostname),
		"--generate-hardware-config", "nixos-facter", facterRelPath,
		"--phases", "",
		"--target-host", fmt.Sprintf("root@%s", b.ip),
	)
	cmd.Dir = b.repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nixos-anywhere generate hardware config: %w", err)
	}

	fmt.Println("Generated facter.json")
	return b.gitAdd(ctx, b.facterPath)
}

func (b *bootstrapper) checkDiskoConfig(ctx context.Context) error {
	// Ensure files are staged before nix eval
	if err := b.gitAdd(ctx, b.secretsPath, b.facterPath, b.sopsConfigPath); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "nix", "eval",
		fmt.Sprintf(".#nixosConfigurations.%s.config.disko.devices.disk.main.device", b.hostname),
		"--raw")
	cmd.Dir = b.repoRoot
	output, err := cmd.Output()
	if err == nil {
		device := strings.TrimSpace(string(output))
		if device != "" {
			fmt.Printf("disko main device: %s\n", device)
			return nil
		}
	}

	// Device not configured - show available disks from facter.json
	fmt.Println("\ndisko.devices.disk.main.device is not configured.")

	facterData, err := os.ReadFile(b.facterPath)
	if err != nil {
		return fmt.Errorf("read facter.json: %w", err)
	}

	var facter struct {
		Hardware struct {
			Disk []struct {
				UnixDeviceNames []string `json:"unix_device_names"`
			} `json:"disk"`
		} `json:"hardware"`
	}
	if err := json.Unmarshal(facterData, &facter); err != nil {
		return fmt.Errorf("parse facter.json: %w", err)
	}

	fmt.Println("\nAvailable disks from hardware detection:")
	for i, disk := range facter.Hardware.Disk {
		fmt.Printf("\nDisk %d:\n", i+1)
		for _, name := range disk.UnixDeviceNames {
			if strings.Contains(name, "/disk/by-id/") {
				fmt.Printf("  %s\n", name)
			}
		}
	}

	fmt.Printf("\nPlease set disko.devices.disk.main.device in flake.nix for host %s.\n", b.hostname)
	fmt.Println("Use a /dev/disk/by-id/ path for stability.")
	fmt.Printf("Then re-run: lab host bootstrap %s --ip %s\n", b.hostname, b.ip)
	return fmt.Errorf("disko.devices.disk.main.device not configured for %s", b.hostname)
}

func (b *bootstrapper) buildAndVerify(ctx context.Context) error {
	// Ensure all files are staged for flake evaluation
	if err := b.gitAdd(ctx, b.secretsPath, b.facterPath, b.sopsConfigPath); err != nil {
		return err
	}

	fmt.Println("Building NixOS configuration...")
	buildCmd := exec.CommandContext(ctx, "nix", "build",
		fmt.Sprintf(".#nixosConfigurations.%s.config.system.build.toplevel", b.hostname),
		"--no-link")
	buildCmd.Dir = b.repoRoot
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("nix build: %w", err)
	}
	fmt.Println("Build successful")

	// Verify stateVersion
	evalCmd := exec.CommandContext(ctx, "nix", "eval",
		fmt.Sprintf(".#nixosConfigurations.%s.config.system.stateVersion", b.hostname),
		"--raw")
	evalCmd.Dir = b.repoRoot
	stateVersion, err := evalCmd.Output()
	if err != nil {
		return fmt.Errorf("evaluate stateVersion: %w", err)
	}
	fmt.Printf("system.stateVersion: %s\n", strings.TrimSpace(string(stateVersion)))

	// Run flake check
	fmt.Println("Running nix flake check...")
	checkCmd := exec.CommandContext(ctx, "nix", "flake", "check")
	checkCmd.Dir = b.repoRoot
	checkCmd.Stdout = os.Stdout
	checkCmd.Stderr = os.Stderr
	if err := checkCmd.Run(); err != nil {
		fmt.Printf("Warning: nix flake check failed: %v\n", err)
		if !confirm("Continue anyway?") {
			return fmt.Errorf("aborted due to flake check failure")
		}
	}

	return nil
}

func (b *bootstrapper) install(ctx context.Context) error {
	fmt.Printf("\nReady to install NixOS on %s at %s\n", b.hostname, b.ip)
	fmt.Println("WARNING: This will format disks and install NixOS on the target machine.")
	if !confirm("Proceed with installation?") {
		fmt.Println("Installation cancelled.")
		return nil
	}

	// Extract SSH host key from encrypted secrets so nixos-anywhere can install
	// it on the target. Without this, the host would generate a new SSH key that
	// doesn't match the age key used to encrypt its secrets.
	extraFilesDir, err := b.prepareExtraFiles(ctx)
	if err != nil {
		return fmt.Errorf("prepare extra files: %w", err)
	}
	defer func() { _ = os.RemoveAll(extraFilesDir) }()

	cmd := exec.CommandContext(ctx, "nixos-anywhere",
		"--extra-files", extraFilesDir,
		"--flake", fmt.Sprintf(".#%s", b.hostname),
		"--target-host", fmt.Sprintf("root@%s", b.ip),
	)
	cmd.Dir = b.repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running nixos-anywhere: %w", err)
	}
	return nil
}

// prepareExtraFiles decrypts secrets.yaml and writes the SSH host key to a
// temporary directory tree that nixos-anywhere will copy to the target via
// --extra-files. The key is placed at persistent/etc/ssh/ssh_host_ed25519_key
// so it ends up at /persistent/etc/ssh/ on the installed system.
func (b *bootstrapper) prepareExtraFiles(ctx context.Context) (string, error) {
	// Decrypt secrets
	decryptCmd := exec.CommandContext(ctx, "sops", "decrypt", b.secretsPath)
	decryptCmd.Dir = b.repoRoot
	output, err := decryptCmd.Output()
	if err != nil {
		return "", fmt.Errorf("decrypt secrets.yaml: %w", err)
	}

	var secrets map[string]string
	if err := yaml.Unmarshal(output, &secrets); err != nil {
		return "", fmt.Errorf("parse decrypted secrets: %w", err)
	}

	privKey := secrets["ssh_host_private_key"]
	pubKey := secrets["ssh_host_public_key"]
	if privKey == "" || pubKey == "" {
		return "", fmt.Errorf("SSH host keys not found in secrets.yaml")
	}

	extraDir, err := os.MkdirTemp("", "lab-extra-files-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}

	sshDir := filepath.Join(extraDir, "persistent", "etc", "ssh")
	if err := os.MkdirAll(sshDir, 0o750); err != nil {
		_ = os.RemoveAll(extraDir)
		return "", fmt.Errorf("creating ssh dir: %w", err)
	}

	privKeyPath := filepath.Join(sshDir, "ssh_host_ed25519_key")
	if err := os.WriteFile(privKeyPath, []byte(privKey), 0o600); err != nil {
		_ = os.RemoveAll(extraDir)
		return "", fmt.Errorf("writing ssh private key: %w", err)
	}

	pubKeyPath := filepath.Join(sshDir, "ssh_host_ed25519_key.pub")
	if err := os.WriteFile(pubKeyPath, []byte(pubKey+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(extraDir)
		return "", fmt.Errorf("writing ssh public key: %w", err)
	}

	return extraDir, nil
}

// --- Helpers ---

func readPassword(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	cmd := exec.CommandContext(ctx, "bash", "-c", `IFS= read -rs pw && printf '%s' "$pw"`)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(out), nil
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// generateSecurePassword creates a random password of the given length
// guaranteed to contain at least one uppercase, lowercase, digit, and special character.
const (
	passwordUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordLower   = "abcdefghijklmnopqrstuvwxyz"
	passwordDigits  = "0123456789"
	passwordSpecial = "!@#$%^&*()-_=+"
	passwordAll     = passwordUpper + passwordLower + passwordDigits + passwordSpecial
)

func generateSecurePassword(length int) (string, error) {
	for {
		pw, err := randomPassword(length)
		if err != nil {
			return "", err
		}
		if hasPasswordComplexity(pw) {
			return string(pw), nil
		}
	}
}

// randomPassword generates a random password of the given length drawn from passwordAll.
func randomPassword(length int) ([]byte, error) {
	pw := make([]byte, length)
	for i := range pw {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAll))))
		if err != nil {
			return nil, fmt.Errorf("generating random index: %w", err)
		}
		pw[i] = passwordAll[n.Int64()]
	}
	return pw, nil
}

// hasPasswordComplexity reports whether pw contains at least one uppercase, lowercase,
// digit, and special character.
func hasPasswordComplexity(pw []byte) bool {
	hasUpper, hasLower, hasDigit, hasSpecial := false, false, false, false
	for _, c := range pw {
		switch {
		case strings.ContainsRune(passwordUpper, rune(c)):
			hasUpper = true
		case strings.ContainsRune(passwordLower, rune(c)):
			hasLower = true
		case strings.ContainsRune(passwordDigits, rune(c)):
			hasDigit = true
		case strings.ContainsRune(passwordSpecial, rune(c)):
			hasSpecial = true
		}
	}
	return hasUpper && hasLower && hasDigit && hasSpecial
}

func (b *bootstrapper) gitAdd(ctx context.Context, filePaths ...string) error {
	args := append([]string{"add", "--"}, filePaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = b.repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (b *bootstrapper) loadSopsConfig() (*yaml.Node, error) {
	data, err := os.ReadFile(b.sopsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read .sops.yaml: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse .sops.yaml: %w", err)
	}
	return &doc, nil
}

func (b *bootstrapper) saveSopsConfig(doc *yaml.Node) error {
	f, err := os.Create(b.sopsConfigPath)
	if err != nil {
		return fmt.Errorf("create .sops.yaml: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode .sops.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close .sops.yaml encoder: %w", err)
	}
	return nil
}

// findCommonKeys extracts the PGP and non-host age keys from the first existing
// host creation rule. These are the "common" keys used for all host secrets.
func (b *bootstrapper) findCommonKeys(root *yaml.Node) (pgpKeys []*yaml.Node, ageKeys []*yaml.Node, err error) {
	rules := findMapValue(root, "creation_rules")
	if rules == nil {
		return nil, nil, fmt.Errorf("no creation_rules in .sops.yaml")
	}

	firstGroup := findHostRuleFirstKeyGroup(rules)
	if firstGroup == nil {
		return nil, nil, fmt.Errorf("no existing host creation rule found in .sops.yaml to derive common keys from")
	}

	if pgp := findMapValue(firstGroup, "pgp"); pgp != nil {
		pgpKeys = resolveAliasNodes(pgp.Content)
	}

	if age := findMapValue(firstGroup, "age"); age != nil {
		for _, resolved := range resolveAliasNodes(age.Content) {
			// Only include non-host keys (user/shared keys)
			if !strings.HasPrefix(resolved.Anchor, "host_") {
				ageKeys = append(ageKeys, resolved)
			}
		}
	}

	return pgpKeys, ageKeys, nil
}

// findHostRuleFirstKeyGroup returns the first key_group of the first nix/hosts/
// creation rule that has one, or nil if no such rule exists.
func findHostRuleFirstKeyGroup(rules *yaml.Node) *yaml.Node {
	for _, rule := range rules.Content {
		pr := findMapValue(rule, "path_regex")
		if pr == nil || !strings.HasPrefix(pr.Value, "nix/hosts/") {
			continue
		}
		kg := findMapValue(rule, "key_groups")
		if kg == nil || len(kg.Content) == 0 {
			continue
		}
		return kg.Content[0]
	}
	return nil
}

// resolveAliasNodes returns nodes with each YAML alias resolved to the node it points to.
func resolveAliasNodes(nodes []*yaml.Node) []*yaml.Node {
	resolved := make([]*yaml.Node, len(nodes))
	for i, k := range nodes {
		if k.Kind == yaml.AliasNode {
			resolved[i] = k.Alias
		} else {
			resolved[i] = k
		}
	}
	return resolved
}

// findMapValue returns the value node for the given key in a YAML mapping node.
func findMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// rootMapping returns the root mapping node from a YAML document node.
func rootMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}
