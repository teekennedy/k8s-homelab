package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.gitea.io/sdk/gitea"
)

func TestMergeStylesValidate(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want []string
	}{
		"shipped": {doc: `
name: k8s-homelab
owner: ops
mergeStyles:
  allowed: [rebase, fast-forward-only, squash]
  default: rebase
`},
		"unknown style": {doc: `
name: r
owner: o
mergeStyles: {allowed: [rebase, ff], default: rebase}
`, want: []string{`unknown style "ff"`}},
		"default not allowed": {doc: `
name: r
owner: o
mergeStyles: {allowed: [squash], default: rebase}
`, want: []string{`default "rebase" must be one of allowed`}},
		"empty": {doc: `
name: r
owner: o
mergeStyles: {allowed: [], default: squash}
`, want: []string{"at least one style", "must be one of allowed"}},
		"duplicate": {doc: `
name: r
owner: o
mergeStyles: {allowed: [squash, squash], default: squash}
`, want: []string{`"squash" listed twice`}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := parseRepository(t, tc.doc).validate()
			if len(errs) != len(tc.want) {
				t.Fatalf("got %d errors %v, want %d", len(errs), errs, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(errs[i].Error(), w) {
					t.Errorf("error %d = %q, want it to contain %q", i, errs[i], w)
				}
			}
		})
	}
}

// fakeRepoAPI serves one repository and records the PATCH body sent to it.
func fakeRepoAPI(t *testing.T, repo gitea.Repository) (*gitea.Client, *map[string]any) {
	t.Helper()
	var patched map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/ops/k8s-homelab" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
				t.Errorf("decode PATCH body: %v", err)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(repo)
	}))
	t.Cleanup(srv.Close)
	c, err := gitea.NewClient(srv.URL, gitea.SetGiteaVersion(""))
	if err != nil {
		t.Fatal(err)
	}
	return c, &patched
}

var shippedStyles = MergeStyles{
	Allowed: []gitea.MergeStyle{gitea.MergeStyleRebase, gitea.MergeStyleFastForwardOnly, gitea.MergeStyleSquash},
	Default: gitea.MergeStyleRebase,
}

// Every flag is sent, so a style dropped from the list is turned off.
func TestSyncMergeStylesPatchesEveryFlag(t *testing.T) {
	c, patched := fakeRepoAPI(t, gitea.Repository{
		AllowMerge:                true,
		AllowRebase:               true,
		AllowFastForwardOnlyMerge: true,
		DefaultMergeStyle:         gitea.MergeStyleMerge,
	})
	if err := syncMergeStyles(c, "ops", "k8s-homelab", shippedStyles); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"allow_merge_commits":           false,
		"allow_rebase":                  true,
		"allow_rebase_explicit":         false,
		"allow_squash_merge":            true,
		"allow_fast_forward_only_merge": true,
		"default_merge_style":           "rebase",
	}
	for k, v := range want {
		if (*patched)[k] != v {
			t.Errorf("PATCH %s = %v, want %v", k, (*patched)[k], v)
		}
	}
}

func TestSyncMergeStylesSkipsWhenInSync(t *testing.T) {
	c, patched := fakeRepoAPI(t, gitea.Repository{
		AllowRebase:               true,
		AllowSquash:               true,
		AllowFastForwardOnlyMerge: true,
		DefaultMergeStyle:         gitea.MergeStyleRebase,
	})
	if err := syncMergeStyles(c, "ops", "k8s-homelab", shippedStyles); err != nil {
		t.Fatal(err)
	}
	if *patched != nil {
		t.Fatalf("expected no PATCH, got %v", *patched)
	}
}
