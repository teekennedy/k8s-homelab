package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The token flow is the brittle part of this job: it drives two HTML forms
// because Woodpecker exposes no API that mints a token for another user. These
// tests stand up a Forgejo and a Woodpecker that behave the way the real ones
// do at each step, so a change here fails in CI rather than at 3am.

const loginPage = `<!DOCTYPE html>
<html><body>
  <form action="/user/search" method="get">
    <input type="hidden" name="q" value="">
  </form>
  <form action="/user/login" method="post">
    <input type="hidden" name="_csrf" value="CSRF-LOGIN">
    <input type="text" name="user_name">
    <input type="password" name="password">
  </form>
</body></html>`

// The real grant page carries more hidden fields than anyone would think to
// enumerate, which is exactly why hiddenInputs resubmits all of them.
const grantPage = `<!DOCTYPE html>
<html><body>
  <form action="/login/oauth/grant" method="post">
    <input type="hidden" name="_csrf" value="CSRF-GRANT">
    <input type="hidden" name="client_id" value="woodpecker-client">
    <input type="hidden" name="state" value="STATE-1">
    <input type="hidden" name="scope" value="">
    <input type="hidden" name="nonce" value="">
    <input type="hidden" name="redirect_uri" value="REDIRECT">
    <button type="submit" name="granted" value="true">Authorize</button>
  </form>
</body></html>`

// forge is a Forgejo stub: web login and the OAuth authorize/grant pair.
//
// It deliberately has no /api/v1/user handler: the real Forgejo rejects
// session-cookie auth on its API routes outright ("token is required"), so
// nothing here may depend on that endpoint working.
type forge struct {
	*httptest.Server
	login, password string
	woodpeckerURL   func() string
	granted         bool
	grantSubmitted  bool
}

func newForge(t *testing.T, login, password string) *forge {
	t.Helper()
	f := &forge{login: login, password: password}
	mux := http.NewServeMux()

	mux.HandleFunc("/user/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeHTML(w, loginPage)
			return
		}
		// A wrong password re-renders the page with HTTP 200 instead of
		// redirecting away from it — the behaviour loginToForgejo detects.
		if r.FormValue("_csrf") != "CSRF-LOGIN" ||
			r.FormValue("user_name") != f.login || r.FormValue("password") != f.password {
			writeHTML(w, loginPage)
			return
		}
		// Not Secure: httptest serves plain HTTP, and a Secure cookie would
		// never be sent back, so the flow under test could not complete.
		//nolint:gosec // G124: test stub over http, not a real session cookie.
		http.SetCookie(w, &http.Cookie{Name: "i_like_gitea", Value: "session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		if !hasCookie(r, "i_like_gitea") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !f.granted {
			writeHTML(w, grantPage)
			return
		}
		http.Redirect(w, r, f.woodpeckerURL()+"/authorize?code=CODE-1&state=STATE-1", http.StatusSeeOther)
	})

	mux.HandleFunc("/login/oauth/grant", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("_csrf") != "CSRF-GRANT" || r.FormValue("client_id") != "woodpecker-client" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.grantSubmitted = true
		f.granted = true
		http.Redirect(w, r, f.woodpeckerURL()+"/authorize?code=CODE-1&state=STATE-1", http.StatusSeeOther)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// ci is a Woodpecker stub: the OAuth entry point, the callback that sets
// user_sess, and the token endpoint that only that cookie unlocks.
type ci struct {
	*httptest.Server
	forgeURL func() string
	token    string
}

func newCI(t *testing.T, token string) *ci {
	t.Helper()
	c := &ci{token: token}
	mux := http.NewServeMux()

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") == "" {
			target := c.forgeURL() + "/login/oauth/authorize?client_id=woodpecker-client&state=STATE-1"
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		//nolint:gosec // G124: test stub over http; see the forge stub above.
		http.SetCookie(w, &http.Cookie{Name: "user_sess", Value: "sess", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("/api/user/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !hasCookie(r, "user_sess") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, c.token)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	c.Server = httptest.NewServer(mux)
	t.Cleanup(c.Close)
	return c
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprint(w, body)
}

func hasCookie(r *http.Request, name string) bool {
	cookie, err := r.Cookie(name)
	return err == nil && cookie.Value != ""
}

// The credentials and token every case here runs with; the interesting
// variation is in how the stubs behave, not in these strings.
const (
	testLogin    = "archon"
	testPassword = "hunter2"
	testToken    = "wp-token-abc"
)

// wire stands up both stubs and points them at each other.
func wire(t *testing.T) (*forge, *ci) {
	t.Helper()
	f := newForge(t, testLogin, testPassword)
	c := newCI(t, testToken)
	f.woodpeckerURL = func() string { return c.URL }
	c.forgeURL = func() string { return f.URL }
	return f, c
}

func TestMintTokenSubmitsTheGrantOnFirstUse(t *testing.T) {
	f, c := wire(t)

	auth, err := newForgeAuth(f.URL, c.URL)
	if err != nil {
		t.Fatalf("newForgeAuth: %v", err)
	}
	got, err := auth.mintToken(context.Background(), testLogin, testPassword)
	if err != nil {
		t.Fatalf("mintToken: %v", err)
	}
	if got != testToken {
		t.Fatalf("token: got %q", got)
	}
	if !f.grantSubmitted {
		t.Fatal("expected the OAuth grant form to have been submitted")
	}
}

// Second and later syncs: Forgejo redirects straight through, and there is no
// grant form on the page to parse.
func TestMintTokenSkipsAnAlreadyGrantedApp(t *testing.T) {
	f, c := wire(t)
	f.granted = true

	auth, _ := newForgeAuth(f.URL, c.URL)
	got, err := auth.mintToken(context.Background(), testLogin, testPassword)
	if err != nil {
		t.Fatalf("mintToken: %v", err)
	}
	if got != testToken {
		t.Fatalf("token: got %q", got)
	}
	if f.grantSubmitted {
		t.Fatal("grant form was submitted for an already-granted app")
	}
}

// The failure mode this guards: Forgejo answers a bad password with HTTP 200
// and the login page again, so the POST succeeding alone proves nothing.
func TestMintTokenRejectsABadPassword(t *testing.T) {
	f, c := wire(t)

	auth, _ := newForgeAuth(f.URL, c.URL)
	_, err := auth.mintToken(context.Background(), testLogin, "wrong")
	if err == nil {
		t.Fatal("expected an error for a wrong password")
	}
	if !strings.Contains(err.Error(), "rejected the login form") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestMintTokenFailsWhenNoSessionCookieIsSet(t *testing.T) {
	f := newForge(t, testLogin, testPassword)
	f.granted = true
	// A Woodpecker that answers the callback without ever setting user_sess.
	// That is the shape of a redirect_uri mismatch, which is exactly what an
	// in-cluster Service URL in the config would produce.
	brokenCI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/authorize" && r.URL.Query().Get("code") == "" {
			http.Redirect(w, r, f.URL+"/login/oauth/authorize?state=STATE-1", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer brokenCI.Close()
	f.woodpeckerURL = func() string { return brokenCI.URL }

	auth, _ := newForgeAuth(f.URL, brokenCI.URL)
	_, err := auth.mintToken(context.Background(), testLogin, testPassword)
	if err == nil || !strings.Contains(err.Error(), "user_sess") {
		t.Fatalf("want a user_sess error, got %v", err)
	}
}

// --- hiddenInputs ----------------------------------------------------------

func TestHiddenInputsPicksTheRightForm(t *testing.T) {
	got, err := hiddenInputs([]byte(loginPage), "/user/login")
	if err != nil {
		t.Fatalf("hiddenInputs: %v", err)
	}
	if got.Get("_csrf") != "CSRF-LOGIN" {
		t.Fatalf("_csrf: got %q", got.Get("_csrf"))
	}
	// The search form above it must not contribute, and non-hidden inputs
	// (user_name, password) are the caller's to set.
	if _, ok := got["q"]; ok {
		t.Fatalf("picked up a field from the wrong form: %v", got)
	}
	if _, ok := got["user_name"]; ok {
		t.Fatalf("picked up a non-hidden input: %v", got)
	}
}

// Resubmitting every hidden field verbatim is what lets this survive Forgejo
// adding one, which it has done before.
func TestHiddenInputsKeepsEveryHiddenField(t *testing.T) {
	got, err := hiddenInputs([]byte(grantPage), "/login/oauth/grant")
	if err != nil {
		t.Fatalf("hiddenInputs: %v", err)
	}
	want := url.Values{
		"_csrf":        {"CSRF-GRANT"},
		"client_id":    {"woodpecker-client"},
		"state":        {"STATE-1"},
		"scope":        {""},
		"nonce":        {""},
		"redirect_uri": {"REDIRECT"},
	}
	if got.Encode() != want.Encode() {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The absence of a grant form is how authorizeWoodpecker detects an
// already-granted app, so "not found" has to be an error and not an empty set.
func TestHiddenInputsErrorsWhenTheFormIsAbsent(t *testing.T) {
	if _, err := hiddenInputs([]byte(loginPage), "/login/oauth/grant"); err == nil {
		t.Fatal("expected an error when the form is absent")
	}
}
