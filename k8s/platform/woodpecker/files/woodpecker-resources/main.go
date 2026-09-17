// Command woodpecker-resources reconciles declarative configuration into a
// running Woodpecker CI server: users and their personal access tokens,
// repository activation and per-repo settings, and repository secrets.
//
// It is the Woodpecker counterpart of the forgejo-resources job in
// k8s/platform/forgejo, and follows the same conventions — one config.yaml
// rendered from chart values, validate everything before touching the server,
// then log-and-continue per entry so one bad entry cannot block the rest.
//
// The one thing it does that has no API behind it is minting a personal access
// token; see forgeauth.go for why that has to drive an OAuth redirect chain.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	ctx := context.Background()

	config, err := loadConfig("./config.yaml")
	if err != nil {
		log.Fatalf("%v", err)
	}

	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("create in-cluster config: %v", err)
	}
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Fatalf("create k8s client: %v", err)
	}

	// Everything below needs an admin token, and the only way to get one is to
	// log in as the bootstrap account. A failure here is fatal rather than
	// logged: there is nothing this job can do without it.
	admin, err := syncIdentity(ctx, k8sClient, config, nil, *config.Bootstrap)
	if err != nil {
		log.Fatalf("bootstrap as %s: %v", config.Bootstrap.Login, err)
	}
	log.Printf("Authenticated as %s", config.Bootstrap.Login)

	syncUsers(ctx, k8sClient, config, admin)
	syncRepositories(ctx, k8sClient, admin, config.Repositories)
}

// loadConfig reads and validates config.yaml.
//
// Validation is fatal and reports every problem at once, mirroring
// forgejo-resources: a malformed config is an operator error that will not fix
// itself on the next sync, and this job is slow enough that finding one typo
// per run would be miserable.
func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // fixed path inside the job's own ConfigMap mount
	if err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("parse config file: %w", err)
	}
	if errs := config.validate(); len(errs) > 0 {
		for _, err := range errs {
			log.Printf("config error: %v", err)
		}
		return Config{}, fmt.Errorf("%d config error(s); refusing to reconcile", len(errs))
	}
	return config, nil
}

// --- identities ------------------------------------------------------------

func syncUsers(ctx context.Context, k8s kubernetes.Interface, config Config, admin *client) {
	for _, user := range config.Users {
		if _, err := syncIdentity(ctx, k8s, config, admin, user); err != nil {
			log.Printf("Sync user %s: %v", user.Login, err)
		}
	}
}

// syncIdentity makes sure identity has a Woodpecker account and that its
// personal access token is in the Secret the config names, and returns a client
// authenticated as it.
//
// admin is nil only for the bootstrap identity, which by definition runs before
// any admin client exists; that account is exempt from the pre-create step
// below because Woodpecker lets an account named in WOODPECKER_ADMIN
// self-register even with registration closed.
func syncIdentity(ctx context.Context, k8s kubernetes.Interface, config Config, admin *client, identity Identity) (*client, error) {
	if admin != nil {
		if err := ensureUser(ctx, admin, identity); err != nil {
			return nil, err
		}
	}

	// A stored token stays valid forever — it is a JWT over the user's `hash`
	// column with no expiry, so POST /api/user/token is deterministic. Reusing
	// it keeps the fragile HTML round trip off the normal sync path entirely:
	// once an identity is provisioned, a Forgejo template change cannot break
	// this job.
	if existing, err := reuseToken(ctx, k8s, config, identity); err != nil {
		log.Printf("Check stored token for %s: %v", identity.Login, err)
	} else if existing != nil {
		log.Printf("Stored token for %s is still valid", identity.Login)
		return existing, nil
	}

	token, err := mintTokenFor(ctx, k8s, config, identity)
	if err != nil {
		return nil, err
	}
	if err := writeSecretKeys(ctx, k8s, identity.TokenSecret.Namespace, identity.TokenSecret.Name,
		map[string]string{identity.TokenSecret.Key: token}); err != nil {
		return nil, err
	}
	log.Printf("Minted a Woodpecker token for %s into %s", identity.Login, identity.TokenSecret)
	return newClient(config.Woodpecker.URL, token), nil
}

// reuseToken returns a client built from the already-stored token if that token
// still authenticates as the right account, or nil.
func reuseToken(ctx context.Context, k8s kubernetes.Interface, config Config, identity Identity) (*client, error) {
	stored, err := readSecretKey(ctx, k8s, identity.TokenSecret)
	if err != nil || stored == "" {
		return nil, err
	}
	candidate := newClient(config.Woodpecker.URL, stored)
	self, err := candidate.self(ctx)
	if err != nil {
		if statusOf(err) == http.StatusUnauthorized {
			// Expected after `DELETE /api/user/token` rotates the hash.
			return nil, nil
		}
		return nil, err
	}
	if !strings.EqualFold(self.Login, identity.Login) {
		// Never log self.Login: it came back over the wire.
		return nil, fmt.Errorf("the token stored in %s belongs to a different account than %s",
			identity.TokenSecret, identity.Login)
	}
	return candidate, nil
}

// mintTokenFor drives the OAuth round trip with the account's Forgejo password.
func mintTokenFor(ctx context.Context, k8s kubernetes.Interface, config Config, identity Identity) (string, error) {
	password, err := readSecretKey(ctx, k8s, SecretRef{
		Name:      identity.CredentialsSecret.Name,
		Namespace: identity.CredentialsSecret.Namespace,
		Key:       "password",
	})
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", fmt.Errorf("no password in %s — has the forgejo-resources job created %s yet?",
			identity.CredentialsSecret, identity.Login)
	}

	auth, err := newForgeAuth(config.Forgejo.URL, config.Woodpecker.URL)
	if err != nil {
		return "", err
	}
	token, err := auth.mintToken(ctx, identity.Login, password)
	if err != nil {
		return "", fmt.Errorf("mint a woodpecker token for %s: %w", identity.Login, err)
	}
	return token, nil
}

// ensureUser pre-creates the Woodpecker account and converges its admin flag.
//
// The pre-create is what makes the account's first OAuth login possible at all:
// with WOODPECKER_OPEN false, server/api/login.go refuses to self-register a
// non-admin. Creating the row first turns that login into a lookup by login
// name, which is allowed.
func ensureUser(ctx context.Context, admin *client, identity Identity) error {
	existing, err := admin.findUser(ctx, identity.Login)
	if err != nil {
		return fmt.Errorf("look up woodpecker user %s: %w", identity.Login, err)
	}

	if existing == nil {
		created, err := admin.createUser(ctx, wpUser{
			Login: identity.Login,
			Email: identity.Email,
		})
		if err != nil {
			return fmt.Errorf("create woodpecker user %s: %w", identity.Login, err)
		}
		log.Printf("Created Woodpecker user %s (id %d)", identity.Login, created.ID)
		existing = &created
	}

	if existing.Admin == identity.Admin {
		return nil
	}
	// WOODPECKER_ADMIN only grants admin at login time and never revokes it, so
	// the flag is asserted here instead of being left to the env var.
	if err := admin.patchUser(ctx, identity.Login, wpUser{
		Login: identity.Login,
		Email: identity.Email,
		Admin: identity.Admin,
	}); err != nil {
		return fmt.Errorf("set admin=%t on woodpecker user %s: %w", identity.Admin, identity.Login, err)
	}
	log.Printf("Set admin=%t on Woodpecker user %s", identity.Admin, identity.Login)
	return nil
}

// --- repositories ----------------------------------------------------------

func syncRepositories(ctx context.Context, k8s kubernetes.Interface, admin *client, repos []Repository) {
	for _, repo := range repos {
		if err := syncRepository(ctx, k8s, admin, repo); err != nil {
			log.Printf("Sync repository %s: %v", repo.fullName(), err)
		}
	}
}

func syncRepository(ctx context.Context, k8s kubernetes.Interface, admin *client, repo Repository) error {
	active, err := resolveRepo(ctx, admin, repo)
	if err != nil {
		return err
	}
	if active == nil {
		return nil
	}

	if err := applyRepoSettings(ctx, admin, active, repo.Settings); err != nil {
		log.Printf("Apply settings to %s: %v", repo.fullName(), err)
	}
	for _, secret := range repo.Secrets {
		if err := syncRepoSecret(ctx, k8s, admin, active.ID, secret); err != nil {
			log.Printf("Sync secret %s on %s: %v", secret.Name, repo.fullName(), err)
		}
	}
	return nil
}

// resolveRepo returns the activated repo, activating it first if the config
// asks for that. A nil result with a nil error means "configured not to
// activate, and not activated" — a state to report, not to fail on.
func resolveRepo(ctx context.Context, admin *client, repo Repository) (*wpRepo, error) {
	existing, err := admin.lookupRepo(ctx, repo.Owner, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", repo.fullName(), err)
	}
	if existing != nil {
		return existing, nil
	}
	if !repo.Activate {
		log.Printf("%s is not activated in Woodpecker and activate is false; skipping", repo.fullName())
		return nil, nil
	}

	remoteID, err := admin.forgeRemoteID(ctx, repo.Owner, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("find the forge id of %s: %w", repo.fullName(), err)
	}
	activated, err := admin.activateRepo(ctx, remoteID)
	if err != nil {
		// A repo deactivated and reactivated out of band can be active in the
		// store but absent from lookup for a beat; treat the conflict as done.
		if statusOf(err) == http.StatusConflict {
			log.Printf("%s is already active", repo.fullName())
			return admin.lookupRepo(ctx, repo.Owner, repo.Name)
		}
		return nil, fmt.Errorf("activate %s: %w", repo.fullName(), err)
	}
	log.Printf("Activated %s in Woodpecker (id %d)", repo.fullName(), activated.ID)
	return &activated, nil
}

// applyRepoSettings PATCHes only the settings that actually differ.
//
// These knobs exist here because Woodpecker's server-wide
// WOODPECKER_DEFAULT_PIPELINE_TIMEOUT and
// WOODPECKER_DEFAULT_CANCEL_PREVIOUS_PIPELINE_EVENTS apply at activation time
// only — an already-activated repo keeps whatever it was given, which used to
// mean editing it by hand in the UI.
func applyRepoSettings(ctx context.Context, admin *client, active *wpRepo, want *RepoSettings) error {
	if want == nil {
		return nil
	}
	patch, drift := repoPatchFor(active, want)
	if len(drift) == 0 {
		log.Printf("%s settings already match", active.FullName)
		return nil
	}
	if err := admin.patchRepo(ctx, active.ID, patch); err != nil {
		return fmt.Errorf("patch repo %d: %w", active.ID, err)
	}
	log.Printf("Reconciled %s settings (%v)", active.FullName, drift)
	return nil
}

// repoPatchFor builds the minimal patch, and names the fields that drifted.
//
// Field NAMES only, never observed values: everything in a wpRepo came back
// over the wire, and interpolating that into a log line is how you get forged
// log entries.
func repoPatchFor(active *wpRepo, want *RepoSettings) (patch wpRepoPatch, drift []string) {
	patch.Timeout = driftedPtr(active.Timeout, want.Timeout)
	patch.Visibility = driftedPtr(active.Visibility, want.Visibility)
	patch.AllowPull = driftedPtr(active.AllowPull, want.AllowPullRequests)
	patch.AllowDeploy = driftedPtr(active.AllowDeploy, want.AllowDeploy)

	for _, field := range []struct {
		name    string
		changed bool
	}{
		{"timeout", patch.Timeout != nil},
		{"visibility", patch.Visibility != nil},
		{"allowPullRequests", patch.AllowPull != nil},
		{"allowDeploy", patch.AllowDeploy != nil},
	} {
		if field.changed {
			drift = append(drift, field.name)
		}
	}

	if want.CancelPreviousPipelineEvents != nil &&
		!sameSet(active.CancelPreviousPipelineEvents, want.CancelPreviousPipelineEvents) {
		events := slices.Clone(want.CancelPreviousPipelineEvents)
		patch.CancelPreviousPipelineEvents = &events
		drift = append(drift, "cancelPreviousPipelineEvents")
	}

	if want.ApprovalAllowedUsers != nil &&
		!sameSet(active.ApprovalAllowedUsers, want.ApprovalAllowedUsers) {
		users := slices.Clone(want.ApprovalAllowedUsers)
		patch.ApprovalAllowedUsers = &users
		drift = append(drift, "approvalAllowedUsers")
	}
	return patch, drift
}

// driftedPtr returns want when it is set and differs from current, and nil
// otherwise — nil being what tells Woodpecker to leave that field alone.
func driftedPtr[T comparable](current T, want *T) *T {
	if want == nil || current == *want {
		return nil
	}
	return want
}

// sameSet compares two string lists as sets: Woodpecker does not promise an
// order, and an order-sensitive comparison would PATCH on every single run.
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a, b := slices.Clone(got), slices.Clone(want)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// --- repository secrets ----------------------------------------------------

// syncRepoSecret converges one repo secret, and mirrors its value into a
// Kubernetes Secret so the workload on the other side of it can verify what CI
// sends.
//
// The Kubernetes Secret is the source of truth, not Woodpecker: Woodpecker
// never returns a secret's value, so nothing here could read it back to compare.
// The value is therefore pushed on every run, which is also the only way a
// rotation on the Kubernetes side ever reaches CI.
func syncRepoSecret(ctx context.Context, k8s kubernetes.Interface, admin *client, repoID int64, secret RepoSecret) error {
	value, err := resolveSecretValue(ctx, k8s, secret)
	if err != nil {
		return err
	}

	existing, err := admin.listRepoSecrets(ctx, repoID)
	if err != nil {
		return fmt.Errorf("list secrets on repo %d: %w", repoID, err)
	}

	desired := wpSecret{
		Name:   secret.Name,
		Value:  value,
		Events: secret.Events,
		Images: secret.Images,
	}
	if slices.ContainsFunc(existing, func(s wpSecret) bool { return s.Name == secret.Name }) {
		if err := admin.updateRepoSecret(ctx, repoID, desired); err != nil {
			return fmt.Errorf("update secret %s: %w", secret.Name, err)
		}
		log.Printf("Reconciled secret %s on repo %d (events %v)", secret.Name, repoID, secret.Events)
		return nil
	}
	if err := admin.createRepoSecret(ctx, repoID, desired); err != nil {
		return fmt.Errorf("create secret %s: %w", secret.Name, err)
	}
	log.Printf("Created secret %s on repo %d (events %v)", secret.Name, repoID, secret.Events)
	return nil
}

// resolveSecretValue reads the mirrored value, generating and storing one on
// first use.
func resolveSecretValue(ctx context.Context, k8s kubernetes.Interface, secret RepoSecret) (string, error) {
	ref := *secret.MirrorSecret
	value, err := readSecretKey(ctx, k8s, ref)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	if secret.Generate == 0 {
		return "", fmt.Errorf("no value at %s key %q, and generate is not set", ref, ref.Key)
	}

	value, err = generateSecret(secret.Generate)
	if err != nil {
		return "", err
	}
	if err := writeSecretKeys(ctx, k8s, ref.Namespace, ref.Name, map[string]string{ref.Key: value}); err != nil {
		return "", err
	}
	log.Printf("Generated a value for secret %s into %s key %q", secret.Name, ref, ref.Key)
	return value, nil
}
