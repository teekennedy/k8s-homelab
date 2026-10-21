// Package enginetest gates the tests that need a real Dagger engine, and
// connects them to it.
//
// Those tests skip unless a run opts in under a Dagger session:
//
//	cd .dagger/modules/homelab && HOMELAB_ENGINE_TESTS=1 dagger run go test ./...
//
// `dagger check ci/integration-test` does the same in a container of its own.
// A Dagger session alone is not enough to opt in: every container Dagger runs
// has one, so go/test would run these too.
package enginetest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	// Importing it fakes a session when there is none, so its RealSession is
	// the one place that knows whether the session in the environment is real.
	"dagger/homelab/internal/cacheprobe"

	"github.com/Khan/genqlient/graphql"
)

// Env opts a test run in to the tests that drive a real engine.
const Env = "HOMELAB_ENGINE_TESTS"

// Require skips a test unless the run opted in with Env=1 under a Dagger
// session.
func Require(t *testing.T) {
	t.Helper()
	if os.Getenv(Env) != "1" {
		t.Skip(Env + " is not set; run `dagger check ci/integration-test` to exercise the real engine")
	}
	if !cacheprobe.RealSession() {
		t.Skip("no Dagger session; run under `" + Env + "=1 dagger run go test ./...`")
	}
}

// Client returns a GraphQL client for the session's engine, skipping the test
// as Require does when there is none. It speaks raw GraphQL, the same
// interface daggerfake implements, so one query can go to either.
//
//nolint:ireturn // graphql.Client is the interface daggerfake also satisfies
func Client(t *testing.T) graphql.Client {
	t.Helper()
	Require(t)

	host := "127.0.0.1:" + os.Getenv("DAGGER_SESSION_PORT")
	token := os.Getenv("DAGGER_SESSION_TOKEN")
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", host)
		},
	}
	return graphql.NewClient(fmt.Sprintf("http://%s/query", host), &http.Client{
		Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
			r.SetBasicAuth(token, "")
			return transport.RoundTrip(r)
		}),
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
