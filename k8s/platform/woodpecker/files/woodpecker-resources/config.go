package main

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// SecretRef points at one key of one Kubernetes Secret.
//
// Namespace is always explicit: this job writes into namespaces other than its
// own (the archon namespace, for one), and a defaulted namespace would make the
// RBAC the chart generates disagree with what the job actually touches.
type SecretRef struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
	Key       string `yaml:"key"`
}

func (s SecretRef) validate(ref string, requireKey bool) []error {
	var errs []error
	if s.Name == "" || s.Namespace == "" {
		errs = append(errs, fmt.Errorf("%s: name and namespace are required", ref))
	}
	if requireKey && s.Key == "" {
		errs = append(errs, fmt.Errorf("%s: key is required", ref))
	}
	return errs
}

func (s SecretRef) String() string { return s.Namespace + "/" + s.Name }

// Endpoint is one of the two services this job drives.
type Endpoint struct {
	URL string `yaml:"url"`
}

// Identity is a Woodpecker user whose personal access token this job mints.
//
// Woodpecker has no admin API for this: a PAT is a JWT signed with the user's
// own `hash` column, which `model.User` tags `json:"-"` and no endpoint ever
// returns. The only issuer is POST /api/user/token, which needs the `user_sess`
// cookie that the browser OAuth round trip sets. So the token is obtained by
// replaying that round trip against Forgejo with the account's password —
// see forgeauth.go. CredentialsSecret is the Secret forgejo-resources wrote it
// into (keys `username` and `password`).
type Identity struct {
	Login string `yaml:"login"`
	Email string `yaml:"email"`
	// Admin mirrors WOODPECKER_ADMIN. It is asserted through the admin API on
	// every sync rather than trusted from the env var, which only takes effect
	// at login time.
	Admin             bool      `yaml:"admin"`
	CredentialsSecret SecretRef `yaml:"credentialsSecret"`
	TokenSecret       SecretRef `yaml:"tokenSecret"`
}

func (i Identity) validate(ref string) []error {
	var errs []error
	if i.Login == "" {
		return append(errs, fmt.Errorf("%s: login is required", ref))
	}
	ref = fmt.Sprintf("%s (%s)", ref, i.Login)
	errs = append(errs, i.CredentialsSecret.validate(ref+" credentialsSecret", false)...)
	errs = append(errs, i.TokenSecret.validate(ref+" tokenSecret", true)...)
	return errs
}

// visibilities are Woodpecker's repo visibility values.
var visibilities = []string{"public", "private", "internal"}

// webhookEvents are the Woodpecker pipeline events a secret or a
// cancel-previous rule may name. Woodpecker rejects anything else with a 400
// that does not say which value was wrong.
var webhookEvents = []string{
	"push", "pull_request", "pull_request_closed",
	"tag", "release", "deployment", "cron", "manual",
}

// RepoSettings are the repo knobs that Woodpecker only exposes per repo, so
// server-wide WOODPECKER_DEFAULT_* env vars cannot reach an already-activated
// repo. Every field is a pointer: an unset field is left alone rather than
// reset to Go's zero value, which for Timeout would mean "no timeout".
type RepoSettings struct {
	Timeout                      *int64   `yaml:"timeout"`
	Visibility                   *string  `yaml:"visibility"`
	AllowPullRequests            *bool    `yaml:"allowPullRequests"`
	AllowDeploy                  *bool    `yaml:"allowDeploy"`
	CancelPreviousPipelineEvents []string `yaml:"cancelPreviousPipelineEvents"`
	// Authors whose pipelines skip the approval gate. Woodpecker blocks a
	// pipeline pending approval per requireApproval mode; naming a bot here
	// exempts that one account without loosening the mode for everyone.
	ApprovalAllowedUsers []string `yaml:"approvalAllowedUsers"`
}

func (r *RepoSettings) validate(ref string) []error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Timeout != nil && *r.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("%s settings: timeout must be positive, got %d", ref, *r.Timeout))
	}
	if r.Visibility != nil && !slices.Contains(visibilities, *r.Visibility) {
		errs = append(errs, fmt.Errorf("%s settings: visibility must be one of %v, got %q", ref, visibilities, *r.Visibility))
	}
	for _, event := range r.CancelPreviousPipelineEvents {
		if !slices.Contains(webhookEvents, event) {
			errs = append(errs, fmt.Errorf("%s settings: cancelPreviousPipelineEvents %q is not one of %v", ref, event, webhookEvents))
		}
	}
	for _, login := range r.ApprovalAllowedUsers {
		if strings.TrimSpace(login) == "" {
			errs = append(errs, fmt.Errorf("%s settings: approvalAllowedUsers must not contain an empty login", ref))
		}
	}
	return errs
}

// woodpeckerSecretName is Woodpecker's own constraint on secret names. It
// lowercases names server-side, so an uppercase name here would reconcile
// forever: the create succeeds, the read back never matches.
var woodpeckerSecretName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

// RepoSecret is one Woodpecker repository secret.
//
// Woodpecker never returns a secret's value, so nothing here can be compared
// against the server. The value is therefore pushed on every sync, exactly like
// forgejo-resources does with webhook secrets: it is the only way a rotation in
// the Kubernetes Secret ever reaches Woodpecker.
type RepoSecret struct {
	Name   string   `yaml:"name"`
	Events []string `yaml:"events"`
	Images []string `yaml:"images"`
	// Generate is the length of a random value to mint on first use. The
	// generated value is stored in MirrorSecret, which is then the source of
	// truth — this job is not the only reader, the workload that verifies the
	// signature is.
	Generate int `yaml:"generate"`
	// MirrorSecret is where the value lives on the Kubernetes side. Required
	// with Generate; optional otherwise, in which case the value is read from
	// there and pushed to Woodpecker.
	MirrorSecret *SecretRef `yaml:"mirrorSecret"`
}

func (s RepoSecret) validate(ref string, seen map[string]bool) []error {
	if s.Name == "" {
		return []error{fmt.Errorf("%s: name is required", ref)}
	}
	var errs []error
	if !woodpeckerSecretName.MatchString(s.Name) {
		errs = append(errs, fmt.Errorf(
			"%s: name %q must match %s — Woodpecker lowercases secret names server-side, so anything else never reconciles",
			ref, s.Name, woodpeckerSecretName))
	}
	ref = fmt.Sprintf("%s (%s)", ref, s.Name)
	if seen[s.Name] {
		errs = append(errs, fmt.Errorf("%s: duplicate secret name", ref))
	}
	seen[s.Name] = true

	errs = append(errs, s.validateEvents(ref)...)
	errs = append(errs, s.validateValueSource(ref)...)
	return errs
}

// validateEvents rejects an empty or misspelled event list. Woodpecker exposes
// a secret only to the events it names, so an empty list is a secret that
// exists and reaches no pipeline.
func (s RepoSecret) validateEvents(ref string) []error {
	var errs []error
	if len(s.Events) == 0 {
		errs = append(errs, fmt.Errorf("%s: events is required — Woodpecker will not expose a secret to any pipeline without it", ref))
	}
	for _, event := range s.Events {
		if !slices.Contains(webhookEvents, event) {
			errs = append(errs, fmt.Errorf("%s: event %q is not one of %v", ref, event, webhookEvents))
		}
	}
	return errs
}

// validateValueSource checks that the secret has exactly one place its value
// can come from. Woodpecker never returns a value, so there is no third option.
func (s RepoSecret) validateValueSource(ref string) []error {
	var errs []error
	switch {
	case s.Generate < 0:
		errs = append(errs, fmt.Errorf("%s: generate must not be negative", ref))
	case s.Generate > 0 && s.MirrorSecret == nil:
		errs = append(errs, fmt.Errorf("%s: generate requires mirrorSecret — a generated value nothing can read back is unusable", ref))
	case s.Generate == 0 && s.MirrorSecret == nil:
		errs = append(errs, fmt.Errorf("%s: mirrorSecret is required — this job has no other source for a secret's value", ref))
	}
	if s.MirrorSecret != nil {
		errs = append(errs, s.MirrorSecret.validate(ref+" mirrorSecret", true)...)
	}
	return errs
}

// Repository is one repo to activate and configure in Woodpecker.
type Repository struct {
	Owner string `yaml:"owner"`
	Name  string `yaml:"name"`
	// Activate registers the repo with Woodpecker if it is not already. False
	// reconciles settings and secrets only, and skips the repo entirely when it
	// has never been activated.
	Activate bool          `yaml:"activate"`
	Settings *RepoSettings `yaml:"settings"`
	Secrets  []RepoSecret  `yaml:"secrets"`
}

func (r Repository) fullName() string { return r.Owner + "/" + r.Name }

func (r Repository) validate(ref string) []error {
	var errs []error
	if r.Owner == "" || r.Name == "" {
		return append(errs, fmt.Errorf("%s: owner and name are required", ref))
	}
	ref = fmt.Sprintf("%s (%s)", ref, r.fullName())
	errs = append(errs, r.Settings.validate(ref)...)
	seen := map[string]bool{}
	for i, secret := range r.Secrets {
		errs = append(errs, secret.validate(fmt.Sprintf("%s secrets[%d]", ref, i), seen)...)
	}
	return errs
}

// Config is the whole declarative input, rendered into config.yaml from the
// `woodpecker-resources` key of the chart's values.
type Config struct {
	Forgejo    Endpoint `yaml:"forgejo"`
	Woodpecker Endpoint `yaml:"woodpecker"`
	// Bootstrap is the identity the job itself authenticates as. It must be
	// listed in WOODPECKER_ADMIN: every admin call below needs it, and it is
	// also the only account that can self-register when WOODPECKER_OPEN is
	// false (server/api/login.go exempts admins from the registration check).
	Bootstrap    *Identity    `yaml:"bootstrap"`
	Users        []Identity   `yaml:"users"`
	Repositories []Repository `yaml:"repositories"`
}

// validate reports every problem at once. A run that dies on the first bad
// entry costs one job run per typo, and this job is slow: each identity it
// provisions is a six-request browser round trip.
func (c Config) validate() []error {
	var errs []error
	errs = append(errs, validateEndpoint("forgejo.url", c.Forgejo.URL)...)
	errs = append(errs, validateEndpoint("woodpecker.url", c.Woodpecker.URL)...)

	if c.Bootstrap == nil {
		errs = append(errs, fmt.Errorf("bootstrap is required: the job has no way to authenticate to Woodpecker without it"))
	} else {
		errs = append(errs, c.Bootstrap.validate("bootstrap")...)
	}

	logins := map[string]bool{}
	if c.Bootstrap != nil {
		logins[c.Bootstrap.Login] = true
	}
	for i, user := range c.Users {
		ref := fmt.Sprintf("users[%d]", i)
		errs = append(errs, user.validate(ref)...)
		if user.Login != "" && logins[user.Login] {
			errs = append(errs, fmt.Errorf("%s (%s): duplicate login", ref, user.Login))
		}
		logins[user.Login] = true
	}

	repos := map[string]bool{}
	for i, repo := range c.Repositories {
		ref := fmt.Sprintf("repositories[%d]", i)
		errs = append(errs, repo.validate(ref)...)
		if repo.Owner != "" && repo.Name != "" {
			if repos[repo.fullName()] {
				errs = append(errs, fmt.Errorf("%s (%s): duplicate repository", ref, repo.fullName()))
			}
			repos[repo.fullName()] = true
		}
	}
	return errs
}

// validateEndpoint rejects anything that is not an absolute http(s) URL.
//
// Both URLs have to be the PUBLIC ones. The token flow is an OAuth redirect
// chain, and Forgejo compares the redirect_uri Woodpecker sends (built from
// WOODPECKER_HOST) against the one registered on the OAuth app — an in-cluster
// Service URL would be rejected as a redirect_uri mismatch.
func validateEndpoint(ref, raw string) []error {
	if raw == "" {
		return []error{fmt.Errorf("%s is required", ref)}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return []error{fmt.Errorf("%s: %q is not a URL: %w", ref, raw, err)}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return []error{fmt.Errorf("%s: %q must be an absolute http(s) URL", ref, raw)}
	}
	if strings.HasSuffix(raw, "/") {
		return []error{fmt.Errorf("%s: %q must not end in a slash", ref, raw)}
	}
	return nil
}
