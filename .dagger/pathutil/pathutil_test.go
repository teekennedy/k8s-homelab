package pathutil

import (
	"testing"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		// Exact match
		{"flake.lock", "flake.lock", true},
		{".yamllint.yaml", ".yamllint.yaml", true},
		{"cmd/lab/go.mod", "cmd/lab/go.mod", true},
		{"flake.lock", "flake.nix", false},

		// "prefix/**" — anything under prefix
		{"k8s/apps/terraria/Chart.yaml", "k8s/**", true},
		{"k8s/platform/crowdsec/files/bootstrap-middleware/bootstrap.py", "k8s/**", true},
		{"terraform/cloudflare/main.tf", "terraform/**", true},
		{"cmd/lab/main.go", "cmd/lab/**", true},
		{"config/foo.cue", "config/**", true},
		{"k8s-other/foo", "k8s/**", false},
		{"terraform", "terraform/**", false},

		// "prefix/**/*.ext" — extension under prefix
		{"config/gen/cluster.cue", "config/**/*.cue", true},
		{"config/deep/nested/file.cue", "config/**/*.cue", true},
		{"config/file.yaml", "config/**/*.cue", false},
		{"other/file.cue", "config/**/*.cue", false},
		{"cmd/lab/main.go", "cmd/lab/**/*.go", true},
		{"cmd/lab/internal/helm/changed.go", "cmd/lab/**/*.go", true},
		{"cmd/lab/go.mod", "cmd/lab/**/*.go", false},

		// "**/*.ext" — extension anywhere
		{"foo.nix", "**/*.nix", true},
		{"deep/nested/file.nix", "**/*.nix", true},
		{"foo.py", "**/*.py", true},
		{"k8s/apps/terraria/files/discord-bot/server.py", "**/*.py", true},
		{"foo.txt", "**/*.py", false},
		{"foo.yaml", "**/*.yaml", true},
		{"deep/nested/config.yml", "**/*.yml", true},

		// "**/name" — exact basename anywhere
		{"pyproject.toml", "**/pyproject.toml", true},
		{"k8s/platform/crowdsec/files/bootstrap-middleware/pyproject.toml", "**/pyproject.toml", true},
		{"uv.lock", "**/uv.lock", true},
		{"deep/nested/uv.lock", "**/uv.lock", true},

		// "*.ext" — basename match
		{"foo.nix", "*.nix", true},
		{"deep/nested/foo.nix", "*.nix", true},
	}

	for _, tt := range tests {
		t.Run(tt.path+"_"+tt.pattern, func(t *testing.T) {
			got := MatchPattern(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("MatchPattern(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestFilterPaths(t *testing.T) {
	paths := []string{
		"cmd/lab/main.go",
		"cmd/lab/internal/helm/changed.go",
		"cmd/lab/go.mod",
		"k8s/apps/terraria/Chart.yaml",
		"k8s/platform/crowdsec/files/bootstrap-middleware/bootstrap.py",
		"config/gen/cluster.cue",
		"terraform/cloudflare/main.tf",
		".yamllint.yaml",
		"flake.lock",
	}

	tests := []struct {
		name     string
		patterns []string
		want     int
	}{
		{"go files", LintGoPatterns, 3},      // main.go, changed.go, go.mod
		{"python", LintPythonPatterns, 1},    // bootstrap.py
		{"cue", LintCuePatterns, 1},          // cluster.cue
		{"yaml", LintYamlPatterns, 2},        // Chart.yaml, .yamllint.yaml
		{"helm", ValidateHelmPatterns, 3},    // Chart.yaml, bootstrap.py (k8s/**), cluster.cue
		{"terraform", ValidateTfPatterns, 1}, // main.tf
		{"nix validate", ValidateNixPatterns, 1}, // flake.lock
		{"build cli", BuildCliPatterns, 3},   // main.go, changed.go, go.mod
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterPaths(paths, tt.patterns)
			if len(got) != tt.want {
				t.Errorf("FilterPaths(%v) returned %d paths %v, want %d", tt.patterns, len(got), got, tt.want)
			}
		})
	}

	// Empty paths returns nil
	if got := FilterPaths(nil, LintGoPatterns); got != nil {
		t.Errorf("FilterPaths(nil, ...) = %v, want nil", got)
	}
}

func TestExcludeDevenvPaths(t *testing.T) {
	paths := []string{
		"flake.nix",
		".devenv/state.nix",
		".devenv.flake.nix",
		"devenv.local.nix",
		"nix/packages.nix",
	}
	got := ExcludeDevenvPaths(paths)
	want := []string{"flake.nix", "nix/packages.nix"}
	if len(got) != len(want) {
		t.Fatalf("ExcludeDevenvPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGoPackagePaths(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "single file",
			paths: []string{"cmd/lab/internal/helm/changed.go"},
			want:  []string{"./internal/helm/..."},
		},
		{
			name:  "root file",
			paths: []string{"cmd/lab/main.go"},
			want:  []string{"./..."},
		},
		{
			name: "deduplication",
			paths: []string{
				"cmd/lab/internal/helm/changed.go",
				"cmd/lab/internal/helm/discover.go",
				"cmd/lab/cmd/ci.go",
			},
			want: []string{"./internal/helm/...", "./cmd/..."},
		},
		{
			name:  "non-go paths ignored",
			paths: []string{"k8s/apps/terraria/Chart.yaml"},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GoPackagePaths(tt.paths, "cmd/lab")
			if len(got) != len(tt.want) {
				t.Fatalf("GoPackagePaths(%v, \"cmd/lab\") = %v, want %v", tt.paths, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTerraformModuleDirs(t *testing.T) {
	paths := []string{
		"terraform/cloudflare/main.tf",
		"terraform/cloudflare/variables.tf",
		"terraform/github/repos.tf",
		"k8s/apps/terraria/Chart.yaml",
	}
	got := TerraformModuleDirs(paths)
	want := []string{"cloudflare", "github"}
	if len(got) != len(want) {
		t.Fatalf("TerraformModuleDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMatchHelmChartDirs(t *testing.T) {
	chartDirs := map[string]bool{
		"k8s/apps/terraria":                     true,
		"k8s/platform/crowdsec":                 true,
		"k8s/foundation/kured":                  true,
	}

	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "file in chart",
			paths: []string{"k8s/apps/terraria/templates/deployment.yaml"},
			want:  []string{"k8s/apps/terraria"},
		},
		{
			name:  "deeply nested file",
			paths: []string{"k8s/platform/crowdsec/files/bootstrap-middleware/bootstrap.py"},
			want:  []string{"k8s/platform/crowdsec"},
		},
		{
			name:  "chart dir itself",
			paths: []string{"k8s/apps/terraria"},
			want:  []string{"k8s/apps/terraria"},
		},
		{
			name:  "cue triggers all",
			paths: []string{"config/gen/cluster.cue"},
			want:  []string{"k8s"},
		},
		{
			name:  "non-k8s path",
			paths: []string{"cmd/lab/main.go"},
			want:  nil,
		},
		{
			name:  "multiple charts",
			paths: []string{
				"k8s/apps/terraria/Chart.yaml",
				"k8s/foundation/kured/files/kured-webhook/server.py",
			},
			want: []string{"k8s/apps/terraria", "k8s/foundation/kured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchHelmChartDirs(tt.paths, chartDirs)
			if len(got) != len(tt.want) {
				t.Fatalf("MatchHelmChartDirs = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMatchPythonProjects(t *testing.T) {
	allDirs := []string{
		"k8s/apps/terraria/files/discord-bot",
		"k8s/foundation/kured/files/kured-webhook",
		"k8s/platform/crowdsec/files/bootstrap-middleware",
	}

	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "empty paths returns all",
			paths: nil,
			want:  allDirs,
		},
		{
			name:  "matching file",
			paths: []string{"k8s/apps/terraria/files/discord-bot/server.py"},
			want:  []string{"k8s/apps/terraria/files/discord-bot"},
		},
		{
			name:  "non-matching path",
			paths: []string{"cmd/lab/main.go"},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchPythonProjects(tt.paths, allDirs)
			if len(got) != len(tt.want) {
				t.Fatalf("MatchPythonProjects = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
