package doccheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummaryProblems(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string // a substring of the only problem, or "" for none
	}{
		{"one sentence", []string{"Runs the module's Go tests.", "+check"}, ""},
		{"details after a blank line", []string{"Runs the tests.", "", "More here.", "+check"}, ""},
		{"no description", []string{"+check"}, "no description"},
		{"no period", []string{"Runs the tests", "+check"}, "period"},
		{"too long", []string{strings.Repeat("a", MaxSummary) + ".", "+check"}, "characters"},
		{"repeats the name", []string{"Test runs the tests.", "+check"}, "repeats"},
		{"runs on", []string{"Runs the tests.", "Then it fails.", "+check"}, "second line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summaryProblems("Test", tc.lines)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no problems, got %q", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("want one problem containing %q, got %q", tc.want, got)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	src := `package main

type M struct{}

// Runs the tests.
// +check
func (m *M) Test() error { return nil }

// Format formats the files
// +generate
func (m *M) Format() error { return nil }

// Not an artifact, so its doc is not held to the format
func (m *M) Helper() {}
`
	if err := os.WriteFile(filepath.Join(dir, "m.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	problems, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("want 2 problems with Format, got %v", problems)
	}
	for _, p := range problems {
		if p.Function != "Format" {
			t.Errorf("want only Format reported, got %v", p)
		}
	}
}
