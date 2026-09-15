package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr[T any](v T) *T { return &v }

// --- repoPatchFor ----------------------------------------------------------

// The patch has to be minimal. Woodpecker applies only the fields present in
// the body, and a patch built from every field would rewrite knobs the config
// says nothing about.
func TestRepoPatchForOnlyIncludesDrift(t *testing.T) {
	active := &wpRepo{
		ID: 7, FullName: "ops/k8s-homelab",
		Timeout: 60, Visibility: "private", AllowPull: true,
		CancelPreviousPipelineEvents: []string{"pull_request", "push"},
	}
	patch, drift := repoPatchFor(active, &RepoSettings{
		Timeout:                      ptr(int64(100)),
		Visibility:                   ptr("private"),
		CancelPreviousPipelineEvents: []string{"pull_request", "push"},
	})

	if len(drift) != 1 || drift[0] != "timeout" {
		t.Fatalf("drift: got %v, want [timeout]", drift)
	}
	if patch.Timeout == nil || *patch.Timeout != 100 {
		t.Fatalf("timeout: got %v", patch.Timeout)
	}
	if patch.Visibility != nil || patch.CancelPreviousPipelineEvents != nil || patch.AllowPull != nil {
		t.Fatalf("patch touched a field that had not drifted: %+v", patch)
	}
}

// Woodpecker promises no ordering on this list. Comparing it order-sensitively
// would PATCH the repo on every single sync.
func TestCancelPreviousEventsCompareAsASet(t *testing.T) {
	active := &wpRepo{CancelPreviousPipelineEvents: []string{"push", "pull_request"}}
	_, drift := repoPatchFor(active, &RepoSettings{
		CancelPreviousPipelineEvents: []string{"pull_request", "push"},
	})
	if len(drift) != 0 {
		t.Fatalf("reordering is not drift, got %v", drift)
	}
}

func TestRepoPatchForDetectsEveryKnob(t *testing.T) {
	active := &wpRepo{Timeout: 60, Visibility: "public", AllowPull: false, AllowDeploy: true}
	patch, drift := repoPatchFor(active, &RepoSettings{
		Timeout:                      ptr(int64(100)),
		Visibility:                   ptr("private"),
		AllowPullRequests:            ptr(true),
		AllowDeploy:                  ptr(false),
		CancelPreviousPipelineEvents: []string{"push"},
	})
	if len(drift) != 5 {
		t.Fatalf("drift: got %v, want all five", drift)
	}
	if patch.AllowPull == nil || !*patch.AllowPull || patch.AllowDeploy == nil || *patch.AllowDeploy {
		t.Fatalf("bool knobs: %+v", patch)
	}
}

// A nil Settings block must not produce a patch at all — an empty RepoPatch
// would still clear cancel_previous_pipeline_events, whose json tag has no
// omitempty upstream.
func TestNilSettingsPatchNothing(t *testing.T) {
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"PATCH /api/repos/7": func(http.ResponseWriter, *http.Request) {
			t.Fatal("patched a repo with no settings configured")
		},
	})
	if err := applyRepoSettings(context.Background(), stub, &wpRepo{ID: 7}, nil); err != nil {
		t.Fatalf("applyRepoSettings: %v", err)
	}
}

// --- reuseToken ------------------------------------------------------------

// Reusing a stored token is what keeps the HTML round trip off the normal sync
// path. If this regresses, every sync re-drives the OAuth chain.
func TestReuseTokenAcceptsAValidStoredToken(t *testing.T) {
	_, stubURL := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/user": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer stored-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeJSON(w, wpUser{ID: 3, Login: "archon"})
		},
	})
	k8s := fake.NewSimpleClientset(secretWith("archon-woodpecker", "token", "stored-token"))

	got, err := reuseToken(context.Background(), k8s, configFor(stubURL), archonIdentity())
	if err != nil {
		t.Fatalf("reuseToken: %v", err)
	}
	if got == nil {
		t.Fatal("a valid stored token was not reused")
	}
}

// After `DELETE /api/user/token` rotates the user's hash the stored token 401s.
// That is an ordinary state, not a failure: mint a fresh one.
func TestReuseTokenDiscardsARevokedToken(t *testing.T) {
	_, stubURL := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/user": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
	})
	k8s := fake.NewSimpleClientset(secretWith("archon-woodpecker", "token", "revoked"))

	got, err := reuseToken(context.Background(), k8s, configFor(stubURL), archonIdentity())
	if err != nil {
		t.Fatalf("a 401 is expected, not an error: %v", err)
	}
	if got != nil {
		t.Fatal("a revoked token was reused")
	}
}

// A Secret that does not exist yet is normal on a cold bootstrap.
func TestReuseTokenHandlesAMissingSecret(t *testing.T) {
	_, stubURL := stubWoodpecker(t, nil)
	got, err := reuseToken(context.Background(), fake.NewSimpleClientset(), configFor(stubURL), archonIdentity())
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
	}
}

// A token that authenticates as somebody else is a configuration accident
// worth stopping on, not something to silently overwrite.
func TestReuseTokenRejectsATokenForAnotherAccount(t *testing.T) {
	_, stubURL := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/user": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, wpUser{ID: 1, Login: "tkennedy"})
		},
	})
	k8s := fake.NewSimpleClientset(secretWith("archon-woodpecker", "token", "someone-elses"))

	_, err := reuseToken(context.Background(), k8s, configFor(stubURL), archonIdentity())
	if err == nil || !strings.Contains(err.Error(), "different account") {
		t.Fatalf("want a different-account error, got %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "tkennedy") {
		t.Fatalf("error interpolates a server-supplied login: %v", err)
	}
}

// --- ensureUser ------------------------------------------------------------

// The pre-create is what makes a non-admin's first OAuth login possible at all
// when WOODPECKER_OPEN is false.
func TestEnsureUserCreatesAMissingAccount(t *testing.T) {
	var created wpUser
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/users":  func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, []wpUser{}) },
		"POST /api/users": func(w http.ResponseWriter, r *http.Request) { decodeInto(t, r, &created); writeJSON(w, created) },
	})

	if err := ensureUser(context.Background(), stub, archonIdentity()); err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if created.Login != "archon" || created.Email != "archon@msng.to" {
		t.Fatalf("created: %+v", created)
	}
}

// WOODPECKER_ADMIN grants admin at login time and never revokes it, so the flag
// is asserted here rather than left to the env var.
func TestEnsureUserConvergesTheAdminFlag(t *testing.T) {
	var patched wpUser
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/users": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []wpUser{{ID: 3, Login: "archon", Admin: true}})
		},
		"PATCH /api/users/archon": func(w http.ResponseWriter, r *http.Request) { decodeInto(t, r, &patched); writeJSON(w, patched) },
		"POST /api/users":         func(http.ResponseWriter, *http.Request) { t.Fatal("re-created an existing user") },
	})

	if err := ensureUser(context.Background(), stub, archonIdentity()); err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
	if patched.Admin {
		t.Fatalf("admin was not revoked: %+v", patched)
	}
}

func TestEnsureUserIsANoOpWhenNothingDrifted(t *testing.T) {
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/users": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []wpUser{{ID: 3, Login: "Archon"}}) // forge casing differs
		},
		"PATCH /api/users/archon": func(http.ResponseWriter, *http.Request) { t.Fatal("patched a user that matched") },
		"POST /api/users":         func(http.ResponseWriter, *http.Request) { t.Fatal("re-created an existing user") },
	})
	if err := ensureUser(context.Background(), stub, archonIdentity()); err != nil {
		t.Fatalf("ensureUser: %v", err)
	}
}

// --- repository secrets ----------------------------------------------------

// Woodpecker never returns a secret's value, so the Kubernetes Secret is the
// source of truth and the value is generated exactly once.
func TestSyncRepoSecretGeneratesAndMirrorsOnFirstUse(t *testing.T) {
	var pushed wpSecret
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/repos/7/secrets":  func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, []wpSecret{}) },
		"POST /api/repos/7/secrets": func(w http.ResponseWriter, r *http.Request) { decodeInto(t, r, &pushed); writeJSON(w, pushed) },
	})
	k8s := fake.NewSimpleClientset()

	secret := RepoSecret{
		Name: "archon_ci_signal_secret", Events: []string{"pull_request"}, Generate: 32,
		MirrorSecret: &SecretRef{Name: "archon-ci-signal", Namespace: "archon", Key: "hmac-secret"},
	}
	if err := syncRepoSecret(context.Background(), k8s, stub, 7, secret); err != nil {
		t.Fatalf("syncRepoSecret: %v", err)
	}

	stored := mirroredValue(t, k8s, "archon", "archon-ci-signal", "hmac-secret")
	if stored == "" {
		t.Fatal("mirror secret has no value")
	}
	// Both sides must hold the same bytes, or the HMAC never verifies.
	if pushed.Value != stored {
		t.Fatalf("woodpecker got %q but the cluster holds %q", pushed.Value, stored)
	}
	if len(pushed.Events) != 1 || pushed.Events[0] != "pull_request" {
		t.Fatalf("events: %v", pushed.Events)
	}
}

// Regenerating on every sync would rotate the shared secret out from under a
// pipeline mid-run.
func TestSyncRepoSecretReusesAnExistingValue(t *testing.T) {
	var pushed wpSecret
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/repos/7/secrets": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []wpSecret{{ID: 1, Name: "archon_ci_signal_secret"}})
		},
		// Already present: converge it rather than create a duplicate.
		"PATCH /api/repos/7/secrets/archon_ci_signal_secret": func(w http.ResponseWriter, r *http.Request) {
			decodeInto(t, r, &pushed)
			writeJSON(w, pushed)
		},
		"POST /api/repos/7/secrets": func(http.ResponseWriter, *http.Request) { t.Fatal("re-created an existing secret") },
	})
	k8s := fake.NewSimpleClientset(secretWith("archon-ci-signal", "hmac-secret", "already-here"))

	secret := RepoSecret{
		Name: "archon_ci_signal_secret", Events: []string{"pull_request"}, Generate: 32,
		MirrorSecret: &SecretRef{Name: "archon-ci-signal", Namespace: "archon", Key: "hmac-secret"},
	}
	if err := syncRepoSecret(context.Background(), k8s, stub, 7, secret); err != nil {
		t.Fatalf("syncRepoSecret: %v", err)
	}
	if pushed.Value != "already-here" {
		t.Fatalf("value was regenerated: got %q", pushed.Value)
	}
}

// --- resolveRepo -----------------------------------------------------------

// Activation needs the forge's own id, which POST /api/repos is the only
// consumer of; it comes from the ?all=true listing, not from Woodpecker's store.
func TestResolveRepoActivatesAnInactiveRepo(t *testing.T) {
	activated := false
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/repos/lookup/ops/k8s-homelab": func(w http.ResponseWriter, _ *http.Request) {
			if activated {
				writeJSON(w, wpRepo{ID: 7, FullName: "ops/k8s-homelab", IsActive: true})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
		"GET /api/user/repos": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []wpRepo{{FullName: "ops/k8s-homelab", ForgeRemoteID: "42"}})
		},
		"POST /api/repos": func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("forge_remote_id"); got != "42" {
				t.Fatalf("forge_remote_id: got %q", got)
			}
			activated = true
			writeJSON(w, wpRepo{ID: 7, FullName: "ops/k8s-homelab", IsActive: true})
		},
	})

	got, err := resolveRepo(context.Background(), stub, Repository{Owner: "ops", Name: "k8s-homelab", Activate: true})
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if got == nil || got.ID != 7 {
		t.Fatalf("got %+v", got)
	}
}

// Not activated and not asked to activate is a state to report, not to fail on.
func TestResolveRepoSkipsWhenActivateIsFalse(t *testing.T) {
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/repos/lookup/ops/k8s-homelab": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"POST /api/repos":                       func(http.ResponseWriter, *http.Request) { t.Fatal("activated a repo with activate: false") },
	})

	got, err := resolveRepo(context.Background(), stub, Repository{Owner: "ops", Name: "k8s-homelab"})
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
	}
}

// A repo deactivated and reactivated out of band can be active in the store but
// absent from lookup for a beat, so the conflict is a state to re-read, not to
// fail on.
func TestResolveRepoTreatsAConflictAsAlreadyActive(t *testing.T) {
	activated := false
	stub, _ := stubWoodpecker(t, map[string]http.HandlerFunc{
		"GET /api/repos/lookup/ops/k8s-homelab": func(w http.ResponseWriter, _ *http.Request) {
			if activated {
				writeJSON(w, wpRepo{ID: 7, FullName: "ops/k8s-homelab", IsActive: true})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		},
		"GET /api/user/repos": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []wpRepo{{FullName: "ops/k8s-homelab", ForgeRemoteID: "42"}})
		},
		"POST /api/repos": func(w http.ResponseWriter, _ *http.Request) {
			activated = true
			w.WriteHeader(http.StatusConflict)
		},
	})

	got, err := resolveRepo(context.Background(), stub, Repository{Owner: "ops", Name: "k8s-homelab", Activate: true})
	if err != nil {
		t.Fatalf("a 409 means already active, not an error: %v", err)
	}
	if got == nil || got.ID != 7 {
		t.Fatalf("the repo was not re-read after the conflict: %+v", got)
	}
}

// --- writeSecretKeys -------------------------------------------------------

// Several of the Secrets this job writes belong to another provisioner —
// archon-forgejo-user is forgejo-resources' — so the write has to add a key
// rather than replace the object.
func TestWriteSecretKeysKeepsKeysItDoesNotOwn(t *testing.T) {
	k8s := fake.NewSimpleClientset(secretWith("archon-forgejo-user", "password", "from-forgejo"))

	if err := writeSecretKeys(context.Background(), k8s, "archon", "archon-forgejo-user",
		map[string]string{"token": "minted"}); err != nil {
		t.Fatalf("writeSecretKeys: %v", err)
	}

	if got := mirroredValue(t, k8s, "archon", "archon-forgejo-user", "password"); got != "from-forgejo" {
		t.Fatalf("the other provisioner's key was dropped: got %q", got)
	}
	if got := mirroredValue(t, k8s, "archon", "archon-forgejo-user", "token"); got != "minted" {
		t.Fatalf("token: got %q", got)
	}
}

// --- helpers ---------------------------------------------------------------

// stubWoodpecker serves the given "METHOD /path" handlers and fails the test on
// any request that is not one of them. It returns a client pointed at the stub
// and the stub's base URL, for the cases that build a whole Config.
func stubWoodpecker(t *testing.T, handlers map[string]http.HandlerFunc) (*client, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if handler, ok := handlers[key]; ok {
			handler(w, r)
			return
		}
		t.Errorf("unexpected request %s", key)
		w.WriteHeader(http.StatusNotImplemented)
	}))
	t.Cleanup(server.Close)
	return newClient(server.URL, "test-token"), server.URL
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func decodeInto(t *testing.T, r *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

// mirroredValue reads a key back out of the fake cluster.
//
// It checks StringData as well as Data because the fake clientset stores
// objects verbatim — only a real API server performs the StringData -> Data
// conversion that writeSecretKeys relies on when it creates a Secret.
func mirroredValue(t *testing.T, k8s *fake.Clientset, namespace, name, key string) string {
	t.Helper()
	secret, err := k8s.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret %s/%s was not written: %v", namespace, name, err)
	}
	if value, ok := secret.Data[key]; ok {
		return string(value)
	}
	return secret.StringData[key]
}

// secretWith builds a pre-existing Secret in the archon namespace, which is
// where every Secret this job writes across a namespace boundary lands.
func secretWith(name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "archon"},
		Data:       map[string][]byte{key: []byte(value)},
	}
}

func configFor(woodpeckerURL string) Config {
	return Config{
		Forgejo:    Endpoint{URL: "https://git.example"},
		Woodpecker: Endpoint{URL: woodpeckerURL},
	}
}

func archonIdentity() Identity {
	return Identity{
		Login:             "archon",
		Email:             "archon@msng.to",
		CredentialsSecret: SecretRef{Name: "archon-forgejo-user", Namespace: "archon"},
		TokenSecret:       SecretRef{Name: "archon-woodpecker", Namespace: "archon", Key: "token"},
	}
}
