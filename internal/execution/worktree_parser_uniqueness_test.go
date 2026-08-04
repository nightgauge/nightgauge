package execution

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sanctionedIssueParsers are the only functions in the tree allowed to turn an
// `issue-NNN` name into an issue number, each for a DISTINCT on-disk
// convention. Two entries is not a loophole in "exactly one parser" — it is the
// admission that there are exactly two conventions, and conflating them would
// be its own defect.
var sanctionedIssueParsers = map[string]string{
	// The worktree-directory convention: "issue-NNN" (extension WorktreeManager)
	// and "<repo>-issue-NNN" (Go execution.Manager). #323 collapsed three copies
	// of this into one.
	"IssueNumberFromWorktreeDir": "internal/execution/worktree_sweep.go",
	// The run-state FILENAME convention: "issue-NNN.json" under .nightgauge/.
	// A different producer and a different shape — a worktree directory never
	// carries the .json suffix, and a state file never carries a repo prefix.
	"extractContextFiles": "internal/cmd/batchfailures/extractor.go",
}

// TestExactlyOneWorktreeIssueParser is #323's acceptance criterion as a
// standing guard. Three independent `issue-NNN` parsers existed before it —
// the scheduler's, `doctor`'s, and `cleanup`'s — and they had already drifted:
// two accepted the "<repo>-issue-NNN" shape the Go execution.Manager produces
// and one did not.
//
// Drift here is silent by construction. A parser that misses a shape reports
// "no worktree for this issue", which reads as "orphaned", which is the input
// to a `docker compose down -v`. Nothing fails; a live run's volumes are simply
// gone (#296).
//
// If this test fails, do not add an exemption. Call
// execution.IssueNumberFromWorktreeDir — or, if the new site genuinely parses a
// different on-disk convention, say which one and why it cannot be the same
// function.
func TestExactlyOneWorktreeIssueParser(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "bin", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // unparseable generated file — not this test's business
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !parsesIssuePrefixToNumber(fn) {
				continue
			}
			if want, sanctioned := sanctionedIssueParsers[fn.Name.Name]; sanctioned && want == rel {
				continue
			}
			offenders = append(offenders, rel+":"+
				fset.Position(fn.Pos()).String()[len(fset.Position(fn.Pos()).Filename)+1:]+
				" ("+fn.Name.Name+")")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("found %d unsanctioned issue-NNN parser(s):\n  %s\n\n"+
			"Route worktree-directory parsing through execution.IssueNumberFromWorktreeDir. "+
			"Independent copies drift silently, and a missed shape reads as \"orphaned\" — "+
			"which is the input to `docker compose down -v` (#296, #323).",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// parsesIssuePrefixToNumber reports whether fn both splits a name on the
// "issue-" literal AND converts the remainder to an integer. Both halves are
// required: matching the literal alone would flag every log line and compose
// project name that merely mentions issue-NNN, while the numeric conversion is
// what makes a site a parser rather than a formatter.
func parsesIssuePrefixToNumber(fn *ast.FuncDecl) bool {
	var splitsOnIssue, convertsToInt bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch pkg.Name {
		case "strings":
			switch sel.Sel.Name {
			case "LastIndex", "Index", "TrimPrefix", "CutPrefix", "HasPrefix", "SplitN", "Split":
				for _, arg := range call.Args {
					if lit, ok := arg.(*ast.BasicLit); ok &&
						lit.Kind == token.STRING && strings.Contains(lit.Value, "issue-") {
						splitsOnIssue = true
					}
				}
			}
		case "strconv":
			if strings.HasPrefix(sel.Sel.Name, "Atoi") || strings.HasPrefix(sel.Sel.Name, "ParseInt") {
				convertsToInt = true
			}
		case "fmt":
			if sel.Sel.Name == "Sscanf" || sel.Sel.Name == "Sscan" {
				convertsToInt = true
			}
		}
		return true
	})
	return splitsOnIssue && convertsToInt
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}
