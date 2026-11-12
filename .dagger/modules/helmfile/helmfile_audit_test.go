package main

import (
	"slices"
	"testing"
)

func TestCustomCheckIDs(t *testing.T) {
	// The METADATA comment nests `id` under `custom`.
	metadata := "#   id: HL-0002\n#   id: HL-0001\n"
	if got, want := customCheckIDs(metadata), []string{"HL-0001", "HL-0002"}; !slices.Equal(got, want) {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestTrivyFixtureProblems(t *testing.T) {
	// HL-0001 fails its fail fixture. The KSV finding on its pass fixture is
	// another check's and does not count.
	const report = `{"Results": [
		{"Target": "HL-0001/fail.yaml", "Misconfigurations": [{"ID": "HL-0001"}]},
		{"Target": "HL-0001/pass.yaml", "Misconfigurations": [{"ID": "KSV-0001"}]}
	]}`
	agreed := []string{"HL-0001/fail.yaml", "HL-0001/pass.yaml"}

	tests := []struct {
		name     string
		checks   []string
		fixtures []string
		report   string
		want     []string
	}{
		{
			name:     "the check and its fixtures agree",
			checks:   []string{"HL-0001"},
			fixtures: agreed,
			report:   report,
		},
		{
			name:     "a check that cannot fail",
			checks:   []string{"HL-0001"},
			fixtures: agreed,
			report:   `{"Results": []}`,
			want:     []string{"HL-0001/fail.yaml: HL-0001 should fail it, and does not"},
		},
		{
			name:     "a check that fails what it should pass",
			checks:   []string{"HL-0001"},
			fixtures: agreed,
			report: `{"Results": [
				{"Target": "HL-0001/fail.yaml", "Misconfigurations": [{"ID": "HL-0001"}]},
				{"Target": "HL-0001/pass.yaml", "Misconfigurations": [{"ID": "HL-0001"}]}
			]}`,
			want: []string{"HL-0001/pass.yaml: HL-0001 should pass it, and does not"},
		},
		{
			name:     "a check with no fixtures",
			checks:   []string{"HL-0001", "HL-0002"},
			fixtures: agreed,
			report:   report,
			want: []string{
				"HL-0002: no fail*.yaml fixture under k8s/trivy/tests/HL-0002",
				"HL-0002: no pass*.yaml fixture under k8s/trivy/tests/HL-0002",
			},
		},
		{
			name:     "fixtures that belong to no check",
			checks:   []string{"HL-0001"},
			fixtures: append([]string{"HL-0001/other.yaml", "HL-0009/fail.yaml"}, agreed...),
			report:   report,
			want: []string{
				"HL-0001/other.yaml: a fixture is named pass*.yaml or fail*.yaml",
				`HL-0009/fail.yaml: no custom check has the ID "HL-0009"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := trivyFixtureProblems(tt.checks, tt.fixtures, tt.report)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("want %q\ngot  %q", tt.want, got)
			}
		})
	}
}
