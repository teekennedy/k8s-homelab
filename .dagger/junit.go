package main

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Just enough of pytest's JUnit XML to say what happened without making the
// reader scroll through a verbose pytest log. The report is also kept as a file
// (see KubernetesIntegrationReports), so this only has to cover the summary:
// the counts, and which cases carried a failure or an error.

// junitReport is the <testsuites> root pytest writes.
type junitReport struct {
	Suites []junitSuite `xml:"testsuite"`
}

// junitSuite is one <testsuite>. pytest writes a single one, but the schema
// allows several and nothing here depends on there being one.
type junitSuite struct {
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Errors   int         `xml:"errors,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

// junitCase is one test. A case carries a <failure> when an assertion failed and
// an <error> when it never got that far — a fixture raising, for instance.
type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Failure   *junitProblem `xml:"failure"`
	Error     *junitProblem `xml:"error"`
}

// junitProblem is the <failure> or <error> element of a case.
type junitProblem struct {
	Message string `xml:"message,attr"`
}

// junitTotals are a report's counts, summed over its suites.
type junitTotals struct {
	Tests    int
	Failures int
	Errors   int
	Skipped  int
}

// String renders the counts as the one line a passing release gets.
func (t junitTotals) String() string {
	return fmt.Sprintf("%d tests, %d failed, %d errored, %d skipped",
		t.Tests, t.Failures, t.Errors, t.Skipped)
}

// parseJUnitReport parses a pytest JUnit XML report.
func parseJUnitReport(contents string) (*junitReport, error) {
	var report junitReport
	if err := xml.Unmarshal([]byte(contents), &report); err != nil {
		return nil, fmt.Errorf("parsing JUnit XML report: %w", err)
	}
	return &report, nil
}

// Totals sums the report's suites.
func (r *junitReport) Totals() junitTotals {
	var t junitTotals
	for _, s := range r.Suites {
		t.Tests += s.Tests
		t.Failures += s.Failures
		t.Errors += s.Errors
		t.Skipped += s.Skipped
	}
	return t
}

// Problems lists the cases that failed or errored, as
// "classname::name: message", most useful part first.
func (r *junitReport) Problems() []string {
	var problems []string
	for _, s := range r.Suites {
		for _, c := range s.Cases {
			problem := c.Failure
			kind := "failed"
			if problem == nil {
				problem, kind = c.Error, "errored"
			}
			if problem == nil {
				continue
			}
			// The message is pytest's one-line reason; multi-line detail lives in
			// the element's text, which the report file keeps and this does not.
			message := strings.TrimSpace(strings.SplitN(problem.Message, "\n", 2)[0])
			problems = append(problems, fmt.Sprintf("%s::%s %s: %s", c.Classname, c.Name, kind, message))
		}
	}
	return problems
}
