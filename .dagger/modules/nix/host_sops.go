// Edits .sops.yaml for a host: its age key, its secrets' creation rule, and
// its access to the shared nix/modules secrets.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"
)

// sopsConfig is a parsed .sops.yaml, edited as a node tree so that anchors,
// aliases and comments survive.
type sopsConfig struct {
	doc yaml.Node
}

func parseSopsConfig(data []byte) (*sopsConfig, error) {
	s := &sopsConfig{}
	if err := yaml.Unmarshal(data, &s.doc); err != nil {
		return nil, fmt.Errorf("parse .sops.yaml: %w", err)
	}
	if s.root().Kind != yaml.MappingNode {
		return nil, errors.New(".sops.yaml is not a mapping")
	}
	return s, nil
}

// Bytes encodes the config with the two-space indent the file uses.
func (s *sopsConfig) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&s.doc); err != nil {
		return nil, fmt.Errorf("encode .sops.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode .sops.yaml: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *sopsConfig) root() *yaml.Node {
	if s.doc.Kind == yaml.DocumentNode && len(s.doc.Content) > 0 {
		return s.doc.Content[0]
	}
	return &s.doc
}

func (s *sopsConfig) keys() (*yaml.Node, error) {
	keys := findMapValue(s.root(), "keys")
	if keys == nil || keys.Kind != yaml.SequenceNode {
		return nil, errors.New("'keys' not found or not a sequence in .sops.yaml")
	}
	return keys, nil
}

func (s *sopsConfig) rules() (*yaml.Node, error) {
	rules := findMapValue(s.root(), "creation_rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return nil, errors.New("'creation_rules' not found or not a sequence in .sops.yaml")
	}
	return rules, nil
}

func hostAnchor(host string) string {
	return "host_" + host
}

func hostSecretsRegex(host string) string {
	return fmt.Sprintf(`nix/hosts/%s/secrets\.yaml`, host)
}

// CommonKeys returns the PGP and age keys every host's secrets are encrypted
// to besides the host's own, read from the first existing host rule.
func (s *sopsConfig) CommonKeys() (pgp, age []string, err error) {
	rules, err := s.rules()
	if err != nil {
		return nil, nil, err
	}
	group := firstHostKeyGroup(rules)
	if group == nil {
		return nil, nil, errors.New("no existing nix/hosts/ creation rule in .sops.yaml to take the common keys from")
	}
	if seq := findMapValue(group, "pgp"); seq != nil {
		for _, k := range resolveAliases(seq.Content) {
			pgp = append(pgp, k.Value)
		}
	}
	if seq := findMapValue(group, "age"); seq != nil {
		for _, k := range resolveAliases(seq.Content) {
			if !strings.HasPrefix(k.Anchor, "host_") {
				age = append(age, k.Value)
			}
		}
	}
	return pgp, age, nil
}

// SetHostAgeKey adds or updates the host's age key, anchored &host_<host>.
func (s *sopsConfig) SetHostAgeKey(host, ageKey string) (bool, error) {
	keys, err := s.keys()
	if err != nil {
		return false, err
	}
	if node := findAnchor(keys, hostAnchor(host)); node != nil {
		if node.Value == ageKey {
			return false, nil
		}
		node.Value = ageKey
		return true, nil
	}
	keys.Content = append(keys.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: ageKey, Anchor: hostAnchor(host)})
	return true, nil
}

// hostEdits records which of ApplyHost's edits changed the config.
type hostEdits struct {
	key, rule, modules bool
}

// Any reports whether the config changed.
func (e hostEdits) Any() bool {
	return e.key || e.rule || e.modules
}

// ModulesRekey reports whether the module secrets need re-keying: the key
// they are encrypted to changed, or the host gained access to them.
func (e hostEdits) ModulesRekey() bool {
	return e.key || e.modules
}

// ApplyHost gives the host its age key, its secrets' creation rule and its
// access to the module secrets.
func (s *sopsConfig) ApplyHost(host, ageKey string) (hostEdits, error) {
	var edits hostEdits
	var err error
	if edits.key, err = s.SetHostAgeKey(host, ageKey); err != nil {
		return edits, err
	}
	if edits.rule, err = s.EnsureHostRule(host); err != nil {
		return edits, err
	}
	edits.modules, err = s.EnsureModulesAccess(host)
	return edits, err
}

// hostKey returns the host's anchored age key.
func (s *sopsConfig) hostKey(host string) (*yaml.Node, error) {
	keys, err := s.keys()
	if err != nil {
		return nil, err
	}
	key := findAnchor(keys, hostAnchor(host))
	if key == nil {
		return nil, fmt.Errorf("host age key &%s not found in .sops.yaml keys", hostAnchor(host))
	}
	return key, nil
}

// EnsureHostRule adds a creation rule for the host's secrets.yaml, encrypted
// to the common keys and the host's own, after the last existing host rule.
func (s *sopsConfig) EnsureHostRule(host string) (bool, error) {
	rules, err := s.rules()
	if err != nil {
		return false, err
	}
	regex := hostSecretsRegex(host)
	if findRule(rules, func(pr string) bool { return pr == regex }) != nil {
		return false, nil
	}
	group := firstHostKeyGroup(rules)
	if group == nil {
		return false, errors.New("no existing nix/hosts/ creation rule in .sops.yaml to take the common keys from")
	}
	hostKey, err := s.hostKey(host)
	if err != nil {
		return false, err
	}
	insertAfterHostRules(rules, newHostRule(regex, group, hostKey))
	return true, nil
}

// newHostRule is a creation rule for regex, encrypted to group's PGP keys,
// its non-host age keys, and hostKey.
func newHostRule(regex string, group, hostKey *yaml.Node) *yaml.Node {
	pgpSeq := &yaml.Node{Kind: yaml.SequenceNode}
	if seq := findMapValue(group, "pgp"); seq != nil {
		for _, k := range resolveAliases(seq.Content) {
			pgpSeq.Content = append(pgpSeq.Content, aliasTo(k))
		}
	}
	ageSeq := &yaml.Node{Kind: yaml.SequenceNode}
	if seq := findMapValue(group, "age"); seq != nil {
		for _, k := range resolveAliases(seq.Content) {
			if !strings.HasPrefix(k.Anchor, "host_") {
				ageSeq.Content = append(ageSeq.Content, aliasTo(k))
			}
		}
	}
	ageSeq.Content = append(ageSeq.Content, aliasTo(hostKey))

	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("path_regex"), scalar(regex),
		scalar("key_groups"),
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Content: []*yaml.Node{
				scalar("pgp"), pgpSeq,
				scalar("age"), ageSeq,
			}},
		}},
	}}
}

// insertAfterHostRules inserts rule after the last nix/hosts/ rule, keeping
// host rules together ahead of the others.
func insertAfterHostRules(rules, rule *yaml.Node) {
	at := 0
	for i, r := range rules.Content {
		if pr := findMapValue(r, "path_regex"); pr != nil && strings.HasPrefix(pr.Value, "nix/hosts/") {
			at = i + 1
		}
	}
	content := make([]*yaml.Node, 0, len(rules.Content)+1)
	content = append(content, rules.Content[:at]...)
	content = append(content, rule)
	rules.Content = append(content, rules.Content[at:]...)
}

// EnsureModulesAccess adds the host's age key to the nix/modules creation
// rule, so the host can decrypt the shared module secrets.
func (s *sopsConfig) EnsureModulesAccess(host string) (bool, error) {
	ageSeq, err := s.modulesAgeKeys()
	if err != nil {
		return false, err
	}
	hostKey, err := s.hostKey(host)
	if err != nil {
		return false, err
	}
	for _, k := range resolveAliases(ageSeq.Content) {
		if k.Anchor == hostKey.Anchor {
			return false, nil
		}
	}
	ageSeq.Content = append(ageSeq.Content, aliasTo(hostKey))
	return true, nil
}

// modulesAgeKeys returns the age keys of the nix/modules creation rule.
func (s *sopsConfig) modulesAgeKeys() (*yaml.Node, error) {
	rules, err := s.rules()
	if err != nil {
		return nil, err
	}
	modules := findRule(rules, func(pr string) bool { return strings.Contains(pr, "nix/modules") })
	if modules == nil {
		return nil, errors.New("no creation rule for nix/modules in .sops.yaml")
	}
	groups := findMapValue(modules, "key_groups")
	if groups == nil || len(groups.Content) == 0 {
		return nil, errors.New("no key_groups in the nix/modules creation rule")
	}
	ageSeq := findMapValue(groups.Content[0], "age")
	if ageSeq == nil {
		return nil, errors.New("no age keys in the nix/modules creation rule")
	}
	return ageSeq, nil
}

// findRule returns the first rule whose path_regex matches.
func findRule(rules *yaml.Node, match func(pathRegex string) bool) *yaml.Node {
	for _, rule := range rules.Content {
		if pr := findMapValue(rule, "path_regex"); pr != nil && match(pr.Value) {
			return rule
		}
	}
	return nil
}

// firstHostKeyGroup returns the first key group of the first nix/hosts/ rule
// that has one.
func firstHostKeyGroup(rules *yaml.Node) *yaml.Node {
	for _, rule := range rules.Content {
		pr := findMapValue(rule, "path_regex")
		if pr == nil || !strings.HasPrefix(pr.Value, "nix/hosts/") {
			continue
		}
		if groups := findMapValue(rule, "key_groups"); groups != nil && len(groups.Content) > 0 {
			return groups.Content[0]
		}
	}
	return nil
}

func findAnchor(seq *yaml.Node, anchor string) *yaml.Node {
	for _, n := range seq.Content {
		if n.Anchor == anchor {
			return n
		}
	}
	return nil
}

func resolveAliases(nodes []*yaml.Node) []*yaml.Node {
	resolved := make([]*yaml.Node, len(nodes))
	for i, n := range nodes {
		if n.Kind == yaml.AliasNode {
			n = n.Alias
		}
		resolved[i] = n
	}
	return resolved
}

func aliasTo(n *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.AliasNode, Value: n.Anchor, Alias: n}
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v}
}

func findMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
