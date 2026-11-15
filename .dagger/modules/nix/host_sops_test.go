package main

import (
	"slices"
	"strings"
	"testing"
)

// sopsFixture has the shape of the repo's .sops.yaml.
const sopsFixture = `keys:
  # GPG encryption subkey
  - &user_a FD5B1635F2218760E010601F4A6E1024D7BBE968
  - &laptop_age age1laptop
  # ssh-ed25519 host keys converted to age
  - &host_borg-0 age1borg0
  - &host_borg-1 age1borg1
creation_rules:
  - path_regex: nix/hosts/borg-0/secrets\.yaml
    key_groups:
      - pgp:
          - *user_a
        age:
          - *laptop_age
          - *host_borg-0
  - path_regex: nix/hosts/borg-1/secrets\.yaml
    key_groups:
      - pgp:
          - *user_a
        age:
          - *laptop_age
          - *host_borg-1
  - path_regex: nix/modules/*/.*\.enc\.yaml
    key_groups:
      - pgp:
          - *user_a
        age:
          - *laptop_age
          - *host_borg-0
          - *host_borg-1
  - path_regex: terraform/.*\.sops\.ya?ml
    key_groups:
      - age:
          - *laptop_age
        pgp:
          - *user_a
`

// applyHost runs every edit bootstrap makes, returning which ones changed the
// config.
func applyHost(t *testing.T, s *sopsConfig, host, ageKey string) []bool {
	t.Helper()
	var changed []bool
	for _, edit := range []func() (bool, error){
		func() (bool, error) { return s.SetHostAgeKey(host, ageKey) },
		func() (bool, error) { return s.EnsureHostRule(host) },
		func() (bool, error) { return s.EnsureModulesAccess(host) },
	} {
		c, err := edit()
		if err != nil {
			t.Fatal(err)
		}
		changed = append(changed, c)
	}
	return changed
}

func TestSopsConfigExistingHostIsUnchanged(t *testing.T) {
	s, err := parseSopsConfig([]byte(sopsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if changed := applyHost(t, s, "borg-1", "age1borg1"); slices.Contains(changed, true) {
		t.Fatalf("edits for a configured host changed the config: %v", changed)
	}
	out, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != sopsFixture {
		t.Fatalf("round trip changed the file:\n%s", out)
	}
}

func TestSopsConfigNewHost(t *testing.T) {
	s, err := parseSopsConfig([]byte(sopsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if changed := applyHost(t, s, "borg-4", "age1borg4"); slices.Contains(changed, false) {
		t.Fatalf("every edit should apply to a new host: %v", changed)
	}
	out, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, want := range []string{
		"  - &host_borg-4 age1borg4\n",
		`  - path_regex: nix/hosts/borg-4/secrets\.yaml
    key_groups:
      - pgp:
          - *user_a
        age:
          - *laptop_age
          - *host_borg-4
`,
		"          - *host_borg-1\n          - *host_borg-4\n  - path_regex: terraform/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The new rule goes after the last host rule, before the modules rule.
	host1 := strings.Index(got, "nix/hosts/borg-1/")
	host4 := strings.Index(got, "nix/hosts/borg-4/")
	modules := strings.Index(got, "nix/modules/")
	if host1 >= host4 || host4 >= modules {
		t.Errorf("rule order borg-1=%d borg-4=%d modules=%d", host1, host4, modules)
	}

	// Applying again is a no-op.
	if changed := applyHost(t, s, "borg-4", "age1borg4"); slices.Contains(changed, true) {
		t.Fatalf("second application changed the config: %v", changed)
	}
}

func TestSopsConfigUpdatesChangedHostKey(t *testing.T) {
	s, err := parseSopsConfig([]byte(sopsFixture))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetHostAgeKey("borg-1", "age1new")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	out, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "&host_borg-1 age1new\n") {
		t.Errorf("key not updated:\n%s", out)
	}
}

func TestSopsConfigCommonKeys(t *testing.T) {
	s, err := parseSopsConfig([]byte(sopsFixture))
	if err != nil {
		t.Fatal(err)
	}
	pgp, age, err := s.CommonKeys()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pgp, []string{"FD5B1635F2218760E010601F4A6E1024D7BBE968"}) {
		t.Errorf("pgp = %v", pgp)
	}
	if !slices.Equal(age, []string{"age1laptop"}) {
		t.Errorf("age = %v", age)
	}
}

func TestSopsConfigWithoutHostRule(t *testing.T) {
	s, err := parseSopsConfig([]byte("keys:\n  - &a age1a\ncreation_rules:\n  - path_regex: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommonKeys(); err == nil {
		t.Error("CommonKeys without a host rule should fail")
	}
}
