package main

import (
	"slices"
	"strings"
	"testing"
)

// A passing pytest report, with the attributes and nesting pytest actually
// writes, so the parser is exercised against the shape it will see.
const greenJUnit = `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" errors="0" failures="0" skipped="0" tests="2" time="4.2" timestamp="2026-10-09T00:00:00" hostname="runner">
    <testcase classname="test_reflection" name="test_configmap_is_reflected_into_the_target_namespace" time="2.1"/>
    <testcase classname="test_reflection" name="test_configmap_update_propagates_to_the_reflected_copy" time="2.1"/>
  </testsuite>
</testsuites>`

func TestParseJUnitReportPassing(t *testing.T) {
	report, err := parseJUnitReport(greenJUnit)
	if err != nil {
		t.Fatalf("parseJUnitReport: %v", err)
	}
	want := junitTotals{Tests: 2}
	if got := report.Totals(); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}
	if got := report.Problems(); len(got) != 0 {
		t.Errorf("Problems() = %q, want none", got)
	}
}

func TestParseJUnitReportProblems(t *testing.T) {
	// A failed assertion and a fixture that raised: pytest records the first as
	// <failure> and the second as <error>, and both have to be reported.
	const failing = `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" errors="1" failures="1" skipped="0" tests="2" time="91.0">
    <testcase classname="test_reflection" name="test_configmap_is_reflected_into_the_target_namespace" time="90.0">
      <failure message="AssertionError: timed out after 90s waiting for reflector to copy&#10;more detail">long traceback</failure>
    </testcase>
    <testcase classname="test_reflection" name="test_configmap_update_propagates_to_the_reflected_copy" time="1.0">
      <error message="RuntimeError: KUBECONFIG is not set.">long traceback</error>
    </testcase>
  </testsuite>
</testsuites>`

	report, err := parseJUnitReport(failing)
	if err != nil {
		t.Fatalf("parseJUnitReport: %v", err)
	}
	want := junitTotals{Tests: 2, Failures: 1, Errors: 1}
	if got := report.Totals(); got != want {
		t.Errorf("Totals() = %+v, want %+v", got, want)
	}

	// Only the message's first line: the traceback stays in the report file.
	wantProblems := []string{
		"test_reflection::test_configmap_is_reflected_into_the_target_namespace failed: " +
			"AssertionError: timed out after 90s waiting for reflector to copy",
		"test_reflection::test_configmap_update_propagates_to_the_reflected_copy errored: " +
			"RuntimeError: KUBECONFIG is not set.",
	}
	if got := report.Problems(); !slices.Equal(got, wantProblems) {
		t.Errorf("Problems() =\n%q\nwant\n%q", got, wantProblems)
	}
}

func TestParseJUnitReportRejectsGarbage(t *testing.T) {
	// pytest writing no report at all — an internal error, say — must not be
	// mistaken for a run with nothing in it.
	if _, err := parseJUnitReport("not xml at all"); err == nil {
		t.Fatal("parseJUnitReport accepted a non-XML report, want an error")
	}
}

func TestJUnitTotalsString(t *testing.T) {
	got := junitTotals{Tests: 3, Failures: 1, Errors: 0, Skipped: 1}.String()
	for _, want := range []string{"3 tests", "1 failed", "0 errored", "1 skipped"} {
		if !strings.Contains(got, want) {
			t.Errorf("junitTotals.String() = %q, want it to mention %q", got, want)
		}
	}
}
