// Package doccheck holds a module's checks and generators to the description
// format `dagger check -l` lists them by.
//
// The listing prints only the first line of a function's doc comment, next to
// the function's name, so that line has to stand alone:
//
//   - it is one complete sentence, ending with a period;
//   - it fits in MaxSummary characters (a generator's is printed after
//     "staleness check: ");
//   - it does not open with the function's own name, which is already printed
//     beside it;
//   - anything more follows a blank comment line.
//
// Each module runs it from a test of its own:
//
//	func TestCheckDocs(t *testing.T) { doccheck.Run(t, ".") }
package doccheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// MaxSummary is the longest a summary line may be, in characters.
const MaxSummary = 64

// pragma matches the Dagger pragmas that make a function a check or a
// generator.
var pragma = regexp.MustCompile(`^\+(check|generate)\b`)

// Problem is one function whose doc comment breaks the format.
type Problem struct {
	Pos      token.Position
	Function string
	Reason   string
}

func (p Problem) String() string {
	return fmt.Sprintf("%s: %s: %s", p.Pos, p.Function, p.Reason)
}

// Run fails t for every check or generator in the Go package in dir whose doc
// comment breaks the format.
func Run(t *testing.T, dir string) {
	t.Helper()
	problems, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// Check returns the problems in the checks and generators of the Go package in
// dir, skipping test and generated files.
func Check(dir string) ([]Problem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	var problems []Problem
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		problems = append(problems, fileProblems(fset, file)...)
	}
	return problems, nil
}

// fileProblems returns the problems in one file's checks and generators.
func fileProblems(fset *token.FileSet, file *ast.File) []Problem {
	if ast.IsGenerated(file) {
		return nil
	}
	var problems []Problem
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		lines := docLines(fn.Doc)
		if !isArtifact(lines) {
			continue
		}
		for _, reason := range summaryProblems(fn.Name.Name, lines) {
			problems = append(problems, Problem{Pos: fset.Position(fn.Pos()), Function: fn.Name.Name, Reason: reason})
		}
	}
	return problems
}

// docLines is a doc comment's text, one entry per line, without the comment
// markers.
func docLines(doc *ast.CommentGroup) []string {
	if doc == nil {
		return nil
	}
	var lines []string
	for _, c := range doc.List {
		text := strings.TrimPrefix(c.Text, "//")
		text = strings.TrimPrefix(text, " ")
		lines = append(lines, text)
	}
	return lines
}

// isArtifact reports whether a doc comment marks its function a check or a
// generator.
func isArtifact(lines []string) bool {
	for _, line := range lines {
		if pragma.MatchString(line) {
			return true
		}
	}
	return false
}

// summaryProblems lists what is wrong with a doc comment's summary line.
func summaryProblems(function string, lines []string) []string {
	if len(lines) == 0 || strings.HasPrefix(lines[0], "+") {
		return []string{"has no description"}
	}
	summary := lines[0]
	var problems []string
	if !strings.HasSuffix(summary, ".") {
		problems = append(problems, fmt.Sprintf("summary %q does not end with a period", summary))
	}
	if n := len([]rune(summary)); n > MaxSummary {
		problems = append(problems, fmt.Sprintf("summary %q is %d characters, more than %d", summary, n, MaxSummary))
	}
	if first, _, _ := strings.Cut(summary, " "); first == function {
		problems = append(problems, fmt.Sprintf("summary %q repeats the function's name", summary))
	}
	if len(lines) > 1 && lines[1] != "" && !strings.HasPrefix(lines[1], "+") {
		problems = append(problems, "summary runs on to a second line; end it there and put the rest after a blank line")
	}
	return problems
}
