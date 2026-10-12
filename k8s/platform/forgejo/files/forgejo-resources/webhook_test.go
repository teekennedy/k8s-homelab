package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"strings"
	"testing"

	"code.gitea.io/sdk/gitea"
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
  - url: https://openhands.msng.to/api/automation/v1/events/00000000-0000-4000-8000-000000000001/forgejo
    type: gitea
    events: [issue_comment, pull_request_comment]
    secretName: openhands-forge-webhook
    secretNamespace: openhands
    secretKey: webhook-secret
`)
	if errs := r.validate(); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if got := r.Webhooks[1].events(); len(got) != 2 {
		t.Fatalf("openhands events: %v", got)
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

// A team with a repository list must not be created as an all-repos team: that
// is the whole point of listing them.
func TestTeamIncludesAllRepositories(t *testing.T) {
	if !(Team{}).includesAllRepositories() {
		t.Fatal("a team with no repositories should cover the whole org")
	}
	if (Team{Repositories: []string{"k8s-homelab"}}).includesAllRepositories() {
		t.Fatal("a team with repositories should be scoped to them")
	}
}

func TestTeamUnits(t *testing.T) {
	got := parseTeam(t, "name: Bots").units()
	if len(got) != len(defaultTeamUnits) {
		t.Fatalf("default units: %v", got)
	}

	got = parseTeam(t, `
name: Bots
units: [repo.code, repo.releases]
`).units()
	if len(got) != 2 || got[0] != gitea.RepoUnitCode || got[1] != gitea.RepoUnitReleases {
		t.Fatalf("configured units: %v", got)
	}
}

func parseTeam(t *testing.T, doc string) Team {
	t.Helper()
	var team Team
	if err := yaml.Unmarshal([]byte(doc), &team); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return team
}

// fakeForgejo serves just the team endpoints and records the calls that matter.
type fakeForgejo struct {
	t            *testing.T
	createdUnits []gitea.RepoUnitType
	addedMembers []string
	teamExists   bool
}

func (f *fakeForgejo) reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		f.t.Errorf("write response: %v", err)
	}
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	if strings.HasPrefix(r.URL.Path, "/api/v1/teams/7/members/") {
		route = r.Method + " member"
	}
	switch route {
	case "GET /api/v1/orgs/ops/teams":
		f.listTeams(w)
	case "POST /api/v1/orgs/ops/teams":
		f.createTeam(w, r)
	case "GET member":
		f.reply(w, http.StatusNotFound, `{}`)
	case "PUT member":
		f.addedMembers = append(f.addedMembers, path.Base(r.URL.Path))
		f.reply(w, http.StatusNoContent, "")
	default:
		f.t.Errorf("unexpected request %s", route)
		f.reply(w, http.StatusNotFound, `{}`)
	}
}

func (f *fakeForgejo) listTeams(w http.ResponseWriter) {
	body := `[]`
	if f.teamExists {
		body = `[{"id": 7, "name": "Bots"}]`
	}
	f.reply(w, http.StatusOK, body)
}

func (f *fakeForgejo) createTeam(w http.ResponseWriter, r *http.Request) {
	var opt gitea.CreateTeamOption
	if err := json.NewDecoder(r.Body).Decode(&opt); err != nil {
		f.t.Errorf("decode create team: %v", err)
	}
	f.createdUnits = opt.Units
	f.reply(w, http.StatusOK, `{"id": 7, "name": "Bots"}`)
}

func newFakeForgejo(t *testing.T, teamExists bool) (*fakeForgejo, *gitea.Client) {
	t.Helper()
	f := &fakeForgejo{t: t, teamExists: teamExists}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := gitea.NewClient(srv.URL, gitea.SetGiteaVersion(""))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func botsOrg() []Organization {
	return []Organization{{Name: "ops", Teams: []Team{{
		Name:    "Bots",
		Units:   []gitea.RepoUnitType{gitea.RepoUnitCode, gitea.RepoUnitReleases},
		Members: []string{"renovate", "release-bot"},
	}}}}
}

// Members are added by syncTeamMembers, after users exist. Adding them while
// the team is created lost any user that was new in the same run.
func TestSyncOrgTeamCreatesWithUnitsButNoMembers(t *testing.T) {
	f, client := newFakeForgejo(t, false)
	syncOrgTeam(client, "ops", botsOrg()[0].Teams[0])

	if len(f.createdUnits) != 2 || f.createdUnits[1] != gitea.RepoUnitReleases {
		t.Fatalf("created with units %v", f.createdUnits)
	}
	if len(f.addedMembers) != 0 {
		t.Fatalf("members added during team creation: %v", f.addedMembers)
	}
}

func TestSyncTeamMembersAddsMissingMembers(t *testing.T) {
	f, client := newFakeForgejo(t, true)
	syncTeamMembers(client, botsOrg())

	if want := []string{"renovate", "release-bot"}; !reflect.DeepEqual(f.addedMembers, want) {
		t.Fatalf("added %v, want %v", f.addedMembers, want)
	}
}
