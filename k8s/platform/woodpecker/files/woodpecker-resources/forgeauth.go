package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// maxPageBytes caps every HTML page this file reads. Forgejo's login and grant
// pages are a few tens of kilobytes; anything larger is a misrouted response,
// not a form.
const maxPageBytes = 4 << 20

// Woodpecker mints a personal access token only for the caller of
// POST /api/user/token, identified by the `user_sess` cookie that the OAuth
// callback sets (server/api/login.go, server/api/user.go). There is no admin
// path to it: the token is a JWT signed with the user's `hash` column, which
// model.User tags `json:"-"`.
//
// So this replays the browser round trip:
//
//	GET  {forgejo}/user/login                     -> _csrf
//	POST {forgejo}/user/login                     -> session cookie
//	GET  {woodpecker}/authorize                   -> redirects to Forgejo
//	  -> {forgejo}/login/oauth/authorize          -> grant page, first time only
//	POST {forgejo}/login/oauth/grant              -> redirects back with ?code=
//	  -> {woodpecker}/authorize?code=..&state=..  -> user_sess cookie
//	GET  {woodpecker}/web-config.js               -> a CSRF token for that session
//	POST {woodpecker}/api/user/token              -> the token
//
// The next-to-last hop exists because Woodpecker's session-cookie auth treats
// itself as a browser and demands CSRF protection on anything but GET/OPTIONS
// (server/router/middleware/session.SetUser, shared/token.CheckCsrf): a JWT
// in the X-CSRF-TOKEN header, signed with the same secret as the session
// cookie. The SPA reads that JWT out of window.WOODPECKER_CSRF, which
// /web-config.js renders fresh for whoever's session cookie fetches it — so
// that is what this replays too.
//
// It is the brittle part of this job — it parses two HTML forms and one JS
// snippet — which is why syncIdentity reuses an already-stored token whenever
// that token still works, and why hiddenInputs is unit tested rather than
// only exercised in anger.
type forgeAuth struct {
	client        *http.Client
	forgejoURL    string
	woodpeckerURL string
}

func newForgeAuth(forgejoURL, woodpeckerURL string) (*forgeAuth, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	return &forgeAuth{
		// A jar per authenticator, never shared: two identities in one jar
		// would silently mint the second token for the first account.
		client:        &http.Client{Jar: jar, Timeout: requestTimeout},
		forgejoURL:    strings.TrimSuffix(forgejoURL, "/"),
		woodpeckerURL: strings.TrimSuffix(woodpeckerURL, "/"),
	}, nil
}

// mintToken runs the whole round trip and returns a Woodpecker personal access
// token for login.
func (a *forgeAuth) mintToken(ctx context.Context, login, password string) (string, error) {
	if err := a.loginToForgejo(ctx, login, password); err != nil {
		return "", err
	}
	if err := a.authorizeWoodpecker(ctx); err != nil {
		return "", err
	}
	return a.createToken(ctx)
}

// loginToForgejo posts the web login form and confirms it was accepted.
func (a *forgeAuth) loginToForgejo(ctx context.Context, login, password string) error {
	loginURL := a.forgejoURL + "/user/login"

	page, err := a.get(ctx, loginURL)
	if err != nil {
		return fmt.Errorf("fetch forgejo login page: %w", err)
	}
	form, err := hiddenInputs(page, "/user/login")
	if err != nil {
		return fmt.Errorf("forgejo login page: %w", err)
	}
	form.Set("user_name", login)
	form.Set("password", password)

	result, err := a.postForm(ctx, loginURL, form)
	if err != nil {
		return fmt.Errorf("post forgejo login form: %w", err)
	}

	// A wrong password re-renders /user/login with HTTP 200 instead of
	// redirecting away from it, so the POST succeeding proves nothing by
	// itself — the presence of that same form in the response is the signal.
	//
	// This used to instead confirm the session against GET /api/v1/user, but
	// Forgejo's API rejects session-cookie auth outright ("token is
	// required": reqToken in its api router, a deliberate CSRF hardening —
	// API routes only take a token or basic auth). That made this step fail
	// on a correct login exactly as it would on a wrong one, so there is no
	// API endpoint left to ask "who did that log in as" here.
	if _, err := hiddenInputs(result, "/user/login"); err == nil {
		return fmt.Errorf("forgejo rejected the login form for %s", login)
	}
	return nil
}

// authorizeWoodpecker walks Woodpecker's /authorize redirect chain, granting
// the OAuth app on the way through if this account has not granted it before.
func (a *forgeAuth) authorizeWoodpecker(ctx context.Context) error {
	page, err := a.get(ctx, a.woodpeckerURL+"/authorize")
	if err != nil {
		return fmt.Errorf("start woodpecker oauth: %w", err)
	}

	// A grant form on the page means this account has not authorized the OAuth
	// app before. Once it has, Forgejo redirects straight back and Woodpecker
	// has already set the session cookie, so there is nothing to submit.
	if grant, err := hiddenInputs(page, "/login/oauth/grant"); err == nil {
		log.Print("Forgejo has not granted the Woodpecker OAuth app for this account yet; submitting the grant")
		if _, err := a.postForm(ctx, a.forgejoURL+"/login/oauth/grant", grant); err != nil {
			return fmt.Errorf("submit forgejo oauth grant: %w", err)
		}
	}

	if !a.hasSessionCookie() {
		return fmt.Errorf("woodpecker did not set a user_sess cookie; the oauth redirect chain did not complete")
	}
	return nil
}

// hasSessionCookie reports whether the jar holds Woodpecker's session cookie
// for the Woodpecker origin.
func (a *forgeAuth) hasSessionCookie() bool {
	parsed, err := url.Parse(a.woodpeckerURL)
	if err != nil {
		return false
	}
	for _, cookie := range a.client.Jar.Cookies(parsed) {
		if cookie.Name == "user_sess" && cookie.Value != "" {
			return true
		}
	}
	return false
}

// createToken exchanges the session for a personal access token. The response
// is the bare token as text/plain, not JSON.
func (a *forgeAuth) createToken(ctx context.Context) (string, error) {
	csrf, err := a.woodpeckerCSRFToken(ctx)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.woodpeckerURL+"/api/user/token", nil)
	if err != nil {
		return "", fmt.Errorf("build create woodpecker token request: %w", err)
	}
	req.Header.Set("X-CSRF-TOKEN", csrf)

	body, err := a.doRequest(req)
	if err != nil {
		return "", fmt.Errorf("create woodpecker token: %w", err)
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("woodpecker returned an empty token")
	}
	return token, nil
}

// woodpeckerCSRFTokenPattern picks the JWT out of the one line in
// /web-config.js that carries it: `window.WOODPECKER_CSRF = "<jwt>";`.
var woodpeckerCSRFTokenPattern = regexp.MustCompile(`WOODPECKER_CSRF\s*=\s*"([^"]*)"`)

// woodpeckerCSRFToken fetches the CSRF token Woodpecker renders for whoever's
// session cookie requests /web-config.js — see the package doc comment above
// for why createToken needs one.
func (a *forgeAuth) woodpeckerCSRFToken(ctx context.Context) (string, error) {
	page, err := a.get(ctx, a.woodpeckerURL+"/web-config.js")
	if err != nil {
		return "", fmt.Errorf("fetch woodpecker web-config.js: %w", err)
	}
	match := woodpeckerCSRFTokenPattern.FindSubmatch(page)
	if match == nil || len(match[1]) == 0 {
		return "", fmt.Errorf("web-config.js carries no CSRF token; the user_sess cookie may not be authenticated")
	}
	return string(match[1]), nil
}

func (a *forgeAuth) get(ctx context.Context, rawURL string) ([]byte, error) {
	return a.do(ctx, http.MethodGet, rawURL, nil, "")
}

func (a *forgeAuth) postForm(ctx context.Context, rawURL string, form url.Values) ([]byte, error) {
	return a.do(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// do issues one request and fails on any non-2xx. Redirects are followed by the
// default policy, which is what walks the OAuth chain.
func (a *forgeAuth) do(ctx context.Context, method, rawURL string, body io.Reader, contentType string) ([]byte, error) {
	// rawURL is built from operator-supplied config, never from a response.
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, rawURL, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return a.doRequest(req)
}

// doRequest sends a prebuilt request and fails on any non-2xx — the part of
// do that createToken also needs, to set a header do has no parameter for.
func (a *forgeAuth) doRequest(req *http.Request) ([]byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", req.Method, req.URL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	return payload, nil
}

// hiddenInputs returns the hidden field name/value pairs of the first form
// whose action contains actionSuffix, or an error if no such form is present.
//
// Resubmitting every hidden field verbatim — rather than naming _csrf,
// client_id, state and the rest one by one — is what lets this survive Forgejo
// adding a field to either form, which it has done before.
func hiddenInputs(page []byte, actionSuffix string) (url.Values, error) {
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	if form := findForm(doc, actionSuffix); form != nil {
		return collectHiddenInputs(form), nil
	}
	return nil, fmt.Errorf("no form posting to %q found on the page", actionSuffix)
}

// findForm walks the document for the first <form> whose action contains
// actionSuffix.
func findForm(node *html.Node, actionSuffix string) *html.Node {
	if node.Type == html.ElementNode && node.Data == "form" &&
		strings.Contains(attr(node, "action"), actionSuffix) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findForm(child, actionSuffix); found != nil {
			return found
		}
	}
	return nil
}

// collectHiddenInputs gathers the named hidden inputs beneath a form node.
func collectHiddenInputs(form *html.Node) url.Values {
	values := url.Values{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "input" &&
			strings.EqualFold(attr(node, "type"), "hidden") {
			if name := attr(node, "name"); name != "" {
				values.Set(name, attr(node, "value"))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(form)
	return values
}

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
