// Package pathutil provides path matching and filtering utilities for CI path filtering.
package pathutil

import (
	"path/filepath"
	"sort"
	"strings"
)

// Path pattern sets for each leaf function.
var (
	LintNixPatterns    = []string{"**/*.nix"}
	LintCuePatterns    = []string{"config/**/*.cue"}
	LintGoPatterns     = []string{"**/*.go", "**/go.mod", "**/go.sum"}
	LintPythonPatterns = []string{"**/*.py", "**/pyproject.toml", "**/uv.lock"}
	LintYamlPatterns   = []string{"**/*.yaml", "**/*.yml", ".yamllint.yaml"}

	ValidateNixPatterns        = []string{"**/*.nix", "flake.lock", "nix/**"}
	ValidateHelmPatterns       = []string{"k8s/**", "config/**/*.cue", ".dagger/scripts/helm-lint.sh"}
	ValidateTfPatterns         = []string{"terraform/**"}
	ValidateWoodpeckerPatterns = []string{".woodpecker/*.yaml"}

	BuildCliPatterns  = []string{"cmd/lab/**"}
	BuildHelmPatterns = []string{"k8s/**", "config/**/*.cue", ".dagger/scripts/helm-template.sh"}

	TestGoPatterns     = []string{"**/*.go", "**/go.mod", "**/go.sum"}
	TestPythonPatterns = []string{"**/*.py", "**/pyproject.toml", "**/uv.lock"}
)

// Composite pattern sets for orchestrator functions.
var (
	AllLintPatterns     = ConcatPatterns(LintNixPatterns, LintCuePatterns, LintGoPatterns, LintPythonPatterns, LintYamlPatterns)
	AllValidatePatterns = ConcatPatterns(ValidateNixPatterns, ValidateHelmPatterns, ValidateTfPatterns, ValidateWoodpeckerPatterns)
	AllBuildPatterns    = ConcatPatterns(BuildCliPatterns, BuildHelmPatterns)
	AllTestPatterns     = ConcatPatterns(TestGoPatterns, TestPythonPatterns)
)

// ConcatPatterns merges multiple pattern slices into one.
func ConcatPatterns(sets ...[]string) []string {
	var result []string
	for _, s := range sets {
		result = append(result, s...)
	}
	return result
}

// MatchPattern checks if a path matches a simple glob-like pattern.
//
// Supported patterns:
//   - "exact/path"       — exact match
//   - "prefix/**"        — any file under prefix/
//   - "prefix/**/*.ext"  — files with extension under prefix/
//   - "**/*.ext"         — files with extension at any depth
//   - "**/name"          — files with exact name at any depth
//   - "*.ext"            — files with extension (basename match)
func MatchPattern(path, pattern string) bool {
	if path == pattern {
		return true
	}

	// "prefix/**" — anything under prefix
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return strings.HasPrefix(path, prefix+"/")
	}

	// "prefix/**/*.ext" — extension match under prefix
	if i := strings.Index(pattern, "/**/"); i >= 0 {
		prefix := pattern[:i]
		suffix := pattern[i+4:]
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
		matched, _ := filepath.Match(suffix, filepath.Base(path))
		return matched
	}

	// "**/*.ext" or "**/name" — match basename anywhere
	if strings.HasPrefix(pattern, "**/") {
		suffix := pattern[3:]
		matched, _ := filepath.Match(suffix, filepath.Base(path))
		return matched
	}

	// "*.ext" — match basename
	if strings.ContainsRune(pattern, '*') || strings.ContainsRune(pattern, '?') {
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		return matched
	}

	return false
}

// FilterPaths returns paths matching any of the given patterns.
// Returns nil if paths is empty.
func FilterPaths(paths []string, patterns []string) []string {
	if len(paths) == 0 {
		return nil
	}
	var result []string
	for _, p := range paths {
		for _, pattern := range patterns {
			if MatchPattern(p, pattern) {
				result = append(result, p)
				break
			}
		}
	}
	return result
}

// ExcludeDevenvPaths removes paths related to devenv configuration.
func ExcludeDevenvPaths(paths []string) []string {
	var result []string
	for _, p := range paths {
		if !strings.Contains(p, ".devenv") && !strings.HasPrefix(p, "devenv.local.") {
			result = append(result, p)
		}
	}
	return result
}

// GoPackagePaths converts Go file paths to unique package paths relative to the given module directory.
// Example: GoPackagePaths(paths, "cmd/lab") with "cmd/lab/internal/helm/changed.go" → "./internal/helm/..."
func GoPackagePaths(paths []string, moduleDir string) []string {
	prefix := moduleDir + "/"
	seen := map[string]bool{}
	var result []string
	for _, p := range paths {
		rel := strings.TrimPrefix(p, prefix)
		if rel == p {
			continue
		}
		dir := filepath.Dir(rel)
		var pkg string
		if dir == "." {
			pkg = "./..."
		} else {
			pkg = "./" + dir + "/..."
		}
		if !seen[pkg] {
			seen[pkg] = true
			result = append(result, pkg)
		}
	}
	return result
}

// TerraformModuleDirs extracts unique module directory names from terraform/ paths.
// Example: "terraform/cloudflare/main.tf" → "cloudflare"
func TerraformModuleDirs(paths []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, p := range paths {
		if !strings.HasPrefix(p, "terraform/") {
			continue
		}
		rel := strings.TrimPrefix(p, "terraform/")
		parts := strings.SplitN(rel, "/", 2)
		if len(parts) > 0 && parts[0] != "" {
			dir := parts[0]
			if !seen[dir] {
				seen[dir] = true
				result = append(result, dir)
			}
		}
	}
	sort.Strings(result)
	return result
}

// MatchHelmChartDirs matches changed paths against a set of known chart directories.
// CUE config paths cause all charts to be returned (["k8s"]).
// chartDirs is the set of all chart directories discovered from the source.
func MatchHelmChartDirs(paths []string, chartDirs map[string]bool) []string {
	// CUE changes affect all charts
	for _, p := range paths {
		if strings.HasPrefix(p, "config/") && strings.HasSuffix(p, ".cue") {
			return []string{"k8s"}
		}
	}

	k8sPaths := FilterPaths(paths, []string{"k8s/**"})
	if len(k8sPaths) == 0 {
		return nil
	}

	// Walk up from each changed path to find its chart
	matched := map[string]bool{}
	for _, p := range k8sPaths {
		// Check if path itself is a chart dir
		if chartDirs[p] {
			matched[p] = true
			continue
		}
		dir := filepath.Dir(p)
		for dir != "." && dir != "k8s" {
			if chartDirs[dir] {
				matched[dir] = true
				break
			}
			dir = filepath.Dir(dir)
		}
	}

	var result []string
	for dir := range matched {
		result = append(result, dir)
	}
	sort.Strings(result)
	return result
}

// MatchPythonProjects matches changed paths against known Python project directories.
// If paths is empty, returns all project dirs.
func MatchPythonProjects(paths []string, allDirs []string) []string {
	if len(paths) == 0 {
		return allDirs
	}

	matched := map[string]bool{}
	for _, p := range paths {
		for _, dir := range allDirs {
			if strings.HasPrefix(p, dir+"/") {
				matched[dir] = true
			}
		}
	}

	var result []string
	for _, dir := range allDirs {
		if matched[dir] {
			result = append(result, dir)
		}
	}
	return result
}

// MatchGoModules matches changed paths against known Go module directories.
// If paths is empty, returns all module dirs.
func MatchGoModules(paths []string, allDirs []string) []string {
	if len(paths) == 0 {
		return allDirs
	}

	matched := map[string]bool{}
	for _, p := range paths {
		for _, dir := range allDirs {
			if strings.HasPrefix(p, dir+"/") || (dir == "." && !strings.Contains(p, "/")) {
				matched[dir] = true
			}
		}
	}

	var result []string
	for _, dir := range allDirs {
		if matched[dir] {
			result = append(result, dir)
		}
	}
	return result
}
