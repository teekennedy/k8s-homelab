package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// requestTimeout bounds every call this job makes. Woodpecker's repo listing
// with ?all=true fans out to the forge, so it is the slowest of them.
const requestTimeout = 60 * time.Second

// maxBodyBytes caps an API response body.
const maxBodyBytes = 8 << 20

// wpUser is the subset of Woodpecker's model.User this job reads or writes.
// Note what is absent: `hash`, which is what a personal access token is signed
// with, is tagged json:"-" upstream and never crosses the API. That absence is
// the whole reason forgeauth.go exists.
type wpUser struct {
	ID    int64  `json:"id,omitempty"`
	Login string `json:"login"`
	Email string `json:"email,omitempty"`
	Admin bool   `json:"admin,omitempty"`
}

// wpRepo is the subset of model.Repo this job reads.
type wpRepo struct {
	ID                           int64    `json:"id,omitempty"`
	ForgeRemoteID                string   `json:"forge_remote_id"`
	Owner                        string   `json:"owner"`
	Name                         string   `json:"name"`
	FullName                     string   `json:"full_name"`
	IsActive                     bool     `json:"active"`
	Timeout                      int64    `json:"timeout,omitempty"`
	Visibility                   string   `json:"visibility"`
	AllowPull                    bool     `json:"allow_pr"`
	AllowDeploy                  bool     `json:"allow_deploy"`
	CancelPreviousPipelineEvents []string `json:"cancel_previous_pipeline_events"`
}

// wpRepoPatch mirrors model.RepoPatch. Pointers throughout: Woodpecker applies
// only the fields present in the body, so a nil field is "leave it alone".
type wpRepoPatch struct {
	Timeout                      *int64    `json:"timeout,omitempty"`
	Visibility                   *string   `json:"visibility,omitempty"`
	AllowPull                    *bool     `json:"allow_pr,omitempty"`
	AllowDeploy                  *bool     `json:"allow_deploy,omitempty"`
	CancelPreviousPipelineEvents *[]string `json:"cancel_previous_pipeline_events,omitempty"`
}

// wpSecret mirrors model.Secret. Value comes back empty on every read —
// Woodpecker does not return secret values — so it is write-only here.
type wpSecret struct {
	ID     int64    `json:"id,omitempty"`
	Name   string   `json:"name"`
	Value  string   `json:"value,omitempty"`
	Events []string `json:"events"`
	Images []string `json:"images"`
}

// client talks to the Woodpecker REST API as one personal access token.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient(baseURL, token string) *client {
	return &client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// apiError carries the status code so callers can distinguish "not there yet"
// from "broken", which matters for 404 (absent) and 409 (already active).
type apiError struct {
	status int
	method string
	path   string
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("woodpecker %s %s: HTTP %d: %s", e.method, e.path, e.status, e.body)
}

func statusOf(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.status
	}
	return 0
}

// do issues one authenticated request and returns the raw body.
func (c *client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal %s %s body: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	// path is built from operator-supplied config, never from a response body.
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{status: resp.StatusCode, method: method, path: path, body: strings.TrimSpace(string(payload))}
	}
	return payload, nil
}

// decode is a free function rather than a method because Go methods cannot
// carry type parameters.
//
//nolint:ireturn // T is a concrete struct or slice at every call site; the type parameter is not an interface boundary.
func decode[T any](payload []byte, method, path string) (T, error) {
	var out T
	if err := json.Unmarshal(payload, &out); err != nil {
		return out, fmt.Errorf("parse %s %s response: %w", method, path, err)
	}
	return out, nil
}

//nolint:ireturn // see decode.
func get[T any](ctx context.Context, c *client, path string) (T, error) {
	var zero T
	payload, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return zero, err
	}
	return decode[T](payload, http.MethodGet, path)
}

// --- users -----------------------------------------------------------------

// self identifies the token holder, and doubles as the check that a stored
// token is still valid.
func (c *client) self(ctx context.Context) (wpUser, error) {
	return get[wpUser](ctx, c, "/api/user")
}

// findUser returns the user with this login, or nil.
//
// Woodpecker has GET /api/users/{login}, but it 404s with a body that does not
// distinguish "no such user" from "not an admin", so the list is walked
// instead — this install has a handful of users.
func (c *client) findUser(ctx context.Context, login string) (*wpUser, error) {
	users, err := get[[]wpUser](ctx, c, "/api/users?perPage=100")
	if err != nil {
		return nil, err
	}
	for i, user := range users {
		if strings.EqualFold(user.Login, login) {
			return &users[i], nil
		}
	}
	return nil, nil
}

// createUser pre-creates a user record so that account can log in at all.
//
// With WOODPECKER_OPEN false, server/api/login.go refuses to self-register a
// non-admin on first OAuth login ("registration closed"). Creating the row
// first turns that first login into a lookup by login name, which is allowed.
// The row starts with forge_remote_id "0"; the login fills it in.
func (c *client) createUser(ctx context.Context, user wpUser) (wpUser, error) {
	payload, err := c.do(ctx, http.MethodPost, "/api/users", user)
	if err != nil {
		return wpUser{}, err
	}
	return decode[wpUser](payload, http.MethodPost, "/api/users")
}

// patchUser converges the mutable fields of an existing user.
func (c *client) patchUser(ctx context.Context, login string, user wpUser) error {
	path := "/api/users/" + url.PathEscape(login)
	_, err := c.do(ctx, http.MethodPatch, path, user)
	return err
}

// --- repositories ----------------------------------------------------------

// lookupRepo returns the ACTIVE repo with this full name, or nil if Woodpecker
// does not have it activated.
func (c *client) lookupRepo(ctx context.Context, owner, name string) (*wpRepo, error) {
	path := fmt.Sprintf("/api/repos/lookup/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	repo, err := get[wpRepo](ctx, c, path)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &repo, nil
}

// forgeRemoteID finds the forge's own id for a repo, which is the only handle
// POST /api/repos accepts. It comes from the token holder's repo list with
// ?all=true, which merges what Woodpecker has stored with what the forge
// reports — so it covers repos that have never been activated.
func (c *client) forgeRemoteID(ctx context.Context, owner, name string) (string, error) {
	repos, err := get[[]wpRepo](ctx, c, "/api/user/repos?all=true")
	if err != nil {
		return "", err
	}
	want := owner + "/" + name
	for _, repo := range repos {
		if strings.EqualFold(repo.FullName, want) {
			return repo.ForgeRemoteID, nil
		}
	}
	return "", fmt.Errorf("%s is not visible to this account on the forge", want)
}

// activateRepo registers a repo with Woodpecker. The forge must report the
// token holder as an ADMIN of the repo, not merely a writer.
func (c *client) activateRepo(ctx context.Context, forgeRemoteID string) (wpRepo, error) {
	path := "/api/repos?forge_remote_id=" + url.QueryEscape(forgeRemoteID)
	payload, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return wpRepo{}, err
	}
	return decode[wpRepo](payload, http.MethodPost, path)
}

func (c *client) patchRepo(ctx context.Context, repoID int64, patch wpRepoPatch) error {
	path := "/api/repos/" + strconv.FormatInt(repoID, 10)
	_, err := c.do(ctx, http.MethodPatch, path, patch)
	return err
}

// --- repository secrets ----------------------------------------------------

func (c *client) listRepoSecrets(ctx context.Context, repoID int64) ([]wpSecret, error) {
	path := fmt.Sprintf("/api/repos/%d/secrets?perPage=100", repoID)
	return get[[]wpSecret](ctx, c, path)
}

func (c *client) createRepoSecret(ctx context.Context, repoID int64, secret wpSecret) error {
	path := fmt.Sprintf("/api/repos/%d/secrets", repoID)
	_, err := c.do(ctx, http.MethodPost, path, secret)
	return err
}

func (c *client) updateRepoSecret(ctx context.Context, repoID int64, secret wpSecret) error {
	path := fmt.Sprintf("/api/repos/%d/secrets/%s", repoID, url.PathEscape(secret.Name))
	_, err := c.do(ctx, http.MethodPatch, path, secret)
	return err
}
