package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Config.validate is the only thing standing between a typo in values.yaml and
// a job that reconciles half of it and logs the rest. Every case below is a
// mistake that Woodpecker itself would accept and then quietly misbehave on.

func parseConfig(t *testing.T, doc string) Config {
	t.Helper()
	var config Config
	if err := yaml.Unmarshal([]byte(doc), &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return config
}

// The shape actually shipped in k8s/platform/woodpecker/values.yaml.
const shippedConfig = `
forgejo:
  url: https://git.msng.to
woodpecker:
  url: https://ci.msng.to
bootstrap:
  login: woodpecker-resources
  email: woodpecker-resources@msng.to
  admin: true
  credentialsSecret:
    name: woodpecker-resources-forgejo-user
    namespace: woodpecker
  tokenSecret:
    name: woodpecker-resources-token
    namespace: woodpecker
    key: token
users:
  - login: archon
    email: archon@msng.to
    credentialsSecret:
      name: archon-forgejo-user
      namespace: archon
    tokenSecret:
      name: archon-woodpecker
      namespace: archon
      key: token
repositories:
  - owner: ops
    name: k8s-homelab
    activate: true
    settings:
      timeout: 100
      cancelPreviousPipelineEvents: [pull_request, push]
    secrets:
      - name: archon_ci_signal_secret
        events: [pull_request]
        generate: 48
        mirrorSecret:
          name: archon-ci-signal
          namespace: archon
          key: hmac-secret
`

func TestValidateAcceptsShippedConfig(t *testing.T) {
	config := parseConfig(t, shippedConfig)
	if errs := config.validate(); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if got := *config.Repositories[0].Settings.Timeout; got != 100 {
		t.Fatalf("timeout: got %d", got)
	}
	if got := config.Repositories[0].Secrets[0].MirrorSecret.String(); got != "archon/archon-ci-signal" {
		t.Fatalf("mirror secret: got %q", got)
	}
}

// An unset pointer must stay unset rather than defaulting: repoPatchFor reads
// nil as "leave this knob alone", and a zero Timeout would mean "no timeout".
func TestUnsetSettingsStayNil(t *testing.T) {
	config := parseConfig(t, `
forgejo: {url: "https://git.example"}
woodpecker: {url: "https://ci.example"}
bootstrap:
  login: bot
  credentialsSecret: {name: c, namespace: n}
  tokenSecret: {name: t, namespace: n, key: token}
repositories:
  - owner: o
    name: r
    settings:
      visibility: private
`)
	if errs := config.validate(); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	settings := config.Repositories[0].Settings
	if settings.Timeout != nil || settings.AllowPullRequests != nil || settings.AllowDeploy != nil {
		t.Fatalf("unset settings should be nil, got %+v", settings)
	}
	if settings.Visibility == nil || *settings.Visibility != "private" {
		t.Fatalf("visibility: got %v", settings.Visibility)
	}
}

// validBase is a config with nothing wrong with it. The cases below that test
// one repository or secret field append to it; the ones that test the preamble
// itself spell out their own document.
const validBase = `
forgejo: {url: "https://git.example"}
woodpecker: {url: "https://ci.example"}
bootstrap:
  login: bot
  credentialsSecret: {name: c, namespace: n}
  tokenSecret: {name: t, namespace: n, key: token}
`

// repoWith wraps one repository stanza in validBase.
func repoWith(body string) string {
	return validBase + "repositories:\n  - owner: o\n    name: r\n" + body
}

// secretEntry wraps one `secrets:` entry in a valid repository.
func secretEntry(entry string) string {
	return repoWith("    secrets:\n      - " + entry + "\n")
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		// Without a bootstrap identity the job cannot authenticate at all.
		"no bootstrap": {`
forgejo: {url: "https://git.example"}
woodpecker: {url: "https://ci.example"}
`, "bootstrap is required"},

		// An in-cluster Service URL here fails as a redirect_uri mismatch
		// halfway through the OAuth chain, which is a miserable way to find out.
		"relative woodpecker url": {`
forgejo: {url: "https://git.example"}
woodpecker: {url: "woodpecker-server:80"}
bootstrap:
  login: bot
  credentialsSecret: {name: c, namespace: n}
  tokenSecret: {name: t, namespace: n, key: token}
`, "must be an absolute http(s) URL"},

		"trailing slash": {`
forgejo: {url: "https://git.example/"}
woodpecker: {url: "https://ci.example"}
bootstrap:
  login: bot
  credentialsSecret: {name: c, namespace: n}
  tokenSecret: {name: t, namespace: n, key: token}
`, "must not end in a slash"},

		"token secret without key": {`
forgejo: {url: "https://git.example"}
woodpecker: {url: "https://ci.example"}
bootstrap:
  login: bot
  credentialsSecret: {name: c, namespace: n}
  tokenSecret: {name: t, namespace: n}
`, "tokenSecret: key is required"},

		// Two entries for one login would mint the second token over the first.
		"duplicate login": {validBase + `
users:
  - login: bot
    credentialsSecret: {name: c, namespace: n}
    tokenSecret: {name: t2, namespace: n, key: token}
`, "duplicate login"},

		"duplicate repository": {validBase + `
repositories:
  - {owner: o, name: r}
  - {owner: o, name: r}
`, "duplicate repository"},

		"bad visibility": {repoWith("    settings: {visibility: secret}\n"), "visibility must be one of"},

		"non-positive timeout": {repoWith("    settings: {timeout: 0}\n"), "timeout must be positive"},

		// Woodpecker lowercases secret names server-side, so an uppercase name
		// is created once and then never matches on read back.
		"uppercase secret name": {secretEntry(
			"{name: CI_SIGNAL, events: [push], generate: 32, mirrorSecret: {name: s, namespace: n, key: k}}",
		), "must match"},

		// A secret with no events is exposed to no pipeline: created, invisible.
		"secret without events": {secretEntry(
			"{name: s, generate: 32, mirrorSecret: {name: s, namespace: n, key: k}}",
		), "events is required"},

		"unknown event": {secretEntry(
			"{name: s, events: [pull_requests], generate: 32, mirrorSecret: {name: s, namespace: n, key: k}}",
		), `event "pull_requests" is not one of`},

		// A generated value nobody can read back cannot be verified against.
		"generate without mirror": {secretEntry(
			"{name: s, events: [push], generate: 32}",
		), "generate requires mirrorSecret"},

		// Neither a value source nor a generator: nothing to push.
		"no value source": {secretEntry(
			"{name: s, events: [push]}",
		), "mirrorSecret is required"},

		"duplicate secret name": {repoWith(`    secrets:
      - {name: s, events: [push], generate: 32, mirrorSecret: {name: a, namespace: n, key: k}}
      - {name: s, events: [push], generate: 32, mirrorSecret: {name: b, namespace: n, key: k}}
`), "duplicate secret name"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var joined []string
			for _, err := range parseConfig(t, tc.doc).validate() {
				joined = append(joined, err.Error())
			}
			if !strings.Contains(strings.Join(joined, "\n"), tc.want) {
				t.Fatalf("want %q in %v", tc.want, joined)
			}
		})
	}
}

// One bad entry must not hide the rest: this job is a slow OAuth round trip per
// identity, and finding one typo per run would be miserable.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	errs := parseConfig(t, `
forgejo: {url: ""}
woodpecker: {url: ""}
`).validate()
	if len(errs) != 3 {
		t.Fatalf("want 3 errors (both urls and the missing bootstrap), got %d: %v", len(errs), errs)
	}
}
