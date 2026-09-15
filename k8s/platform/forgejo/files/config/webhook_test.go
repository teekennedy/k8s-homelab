package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Repository.validate is the only thing standing between a typo in values.yaml
// and a webhook that registers cleanly, then fails every delivery at the
// receiver with an HMAC mismatch. Worth pinning.

func parseRepository(t *testing.T, doc string) Repository {
	t.Helper()
	var r Repository
	if err := yaml.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return r
}

// The shape actually shipped in k8s/platform/forgejo/values.yaml.
func TestValidateAcceptsShippedConfig(t *testing.T) {
	r := parseRepository(t, `
name: k8s-homelab
owner: ops
webhooks:
  - url: http://argocd-server.argocd.svc.cluster.local/api/webhook
    type: gogs
    events: [push]
    branchFilter: main
    secretName: argocd-secret
    secretNamespace: argocd
    secretKey: webhook.gogs.secret
  - url: https://archon.msng.to/webhooks/gitea
    type: gitea
    events: [issues, issue_comment, pull_request, pull_request_comment]
    secretName: archon-webhook
    secretNamespace: archon
    secretKey: webhook-secret
`)
	if errs := r.validate(); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if got := r.Webhooks[1].events(); len(got) != 4 {
		t.Fatalf("archon events: %v", got)
	}
}

// Omitted events keep the single-hook form's old behaviour.
func TestEventsDefaultsToPush(t *testing.T) {
	got := RepoWebhook{}.events()
	if len(got) != 1 || got[0] != "push" {
		t.Fatalf("got %v", got)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"retired singular key": {`
name: r
owner: o
webhook: {url: "http://x", secretName: s, secretNamespace: n, secretKey: k}
`, "no longer supported"},
		"missing type": {`
name: r
owner: o
webhooks:
  - {url: "http://x", secretName: s, secretNamespace: n, secretKey: k}
`, "type must be one of"},
		// The SDK has slack/discord/telegram/... types, but they take entirely
		// different Config keys and nothing here populates them.
		"chat hook type": {`
name: r
owner: o
webhooks:
  - {url: "http://x", type: slack, secretName: s, secretNamespace: n, secretKey: k}
`, "type must be one of"},
		"missing url": {`
name: r
owner: o
webhooks:
  - {type: gitea, secretName: s, secretNamespace: n, secretKey: k}
`, "url is required"},
		// Two entries for one URL reconcile the same hook twice, last one winning.
		"duplicate url": {`
name: r
owner: o
webhooks:
  - {url: "http://x", type: gitea, secretName: s, secretNamespace: n, secretKey: k}
  - {url: "http://x", type: gogs, secretName: s, secretNamespace: n, secretKey: k}
`, "duplicate url"},
		"missing secret coordinates": {`
name: r
owner: o
webhooks:
  - {url: "http://x", type: gitea}
`, "secretName, secretNamespace and secretKey are all required"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var joined []string
			for _, err := range parseRepository(t, tc.doc).validate() {
				joined = append(joined, err.Error())
			}
			if !strings.Contains(strings.Join(joined, "\n"), tc.want) {
				t.Fatalf("want %q in %v", tc.want, joined)
			}
		})
	}
}
