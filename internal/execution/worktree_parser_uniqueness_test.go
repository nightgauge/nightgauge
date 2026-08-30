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
	offenders := scanForIssueParsers(t, moduleRoot(t))
	if len(offenders) > 0 {
		t.Errorf("found %d unsanctioned issue-NNN parser(s):\n  %s\n\n"+
			"Route worktree-directory parsing through execution.IssueNumberFromWorktreeDir. "+
			"Independent copies drift silently, and a missed shape reads as \"orphaned\" — "+
			"which is the input to `docker compose down -v` (#296, #323).",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// scanForIssueParsers walks `root` and returns every issue-NNN parser that is
// not the sanctioned one at its sanctioned path. Extracted from the test body
// so the directory-exclusion behaviour can be exercised against a fixture tree
// (#851) — a guard whose own skip list is untested is how `.worktrees` went
// unnoticed.
func scanForIssueParsers(t *testing.T, root string) []string {
	t.Helper()

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
			// A nested checkout of THIS repo contains every sanctioned parser
			// again, at a path that is not its sanctioned path — so scanning
			// one makes this guard report IssueNumberFromWorktreeDir, the
			// function its own failure message tells you to route through, as
			// an unsanctioned copy of itself, once per live worktree. That is
			// #851, and `ci-local.sh` cannot pass while any worktree exists.
			//
			// #851 excluded the ONE directory name in use at the time
			// (`.worktrees`). The agent worktree convention puts checkouts
			// under `.claude/worktrees/` instead — a directory named
			// `worktrees`, which the literal `.worktrees` entry does not match
			// — so the identical defect came back at a new path (#1200).
			//
			// Detect the PROPERTY instead of enumerating names: a directory
			// that contains its own `.git` is a checkout, not this checkout's
			// source. That covers both conventions in use, any future one, and
			// a hand-made `git worktree add` anywhere in the tree.
			if path != root && isNestedCheckout(path) {
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
	return offenders
}

// isNestedCheckout reports whether dir is the root of a git checkout.
//
// `git worktree add` writes a `.git` FILE (a gitdir pointer) rather than a
// directory, and a plain clone writes a directory — os.Stat accepts both
// without caring which, because either one means the same thing here: the
// files below this point belong to another checkout's working tree, not to the
// tree being scanned (#1200).
func isNestedCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
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

// TestIssueParserScanSkipsNestedWorktrees is #851 as a standing guard.
//
// A pipeline worktree is a nested checkout of THIS repo under `.worktrees/`,
// so every sanctioned parser exists again inside it at a path that is not its
// sanctioned path. Scanning them made the uniqueness guard report
// IssueNumberFromWorktreeDir — the function its own failure message tells you
// to route through — as an unsanctioned copy of itself, once per live
// worktree, and `ci-local.sh` could not pass while the pipeline held one.
func TestIssueParserScanSkipsNestedWorktrees(t *testing.T) {
	root := t.TempDir()

	// The shape the detector matches: split on the "issue-" literal AND
	// convert the remainder to an integer.
	parser := `package fixture

import (
	"strconv"
	"strings"
)

func ParseIssueDir(base string) (int, bool) {
	idx := strings.LastIndex(base, "issue-")
	if idx < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[idx+len("issue-"):])
	if err != nil {
		return 0, false
	}
	return n, true
}
`
	write := func(rel string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(parser), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// A nested checkout is what it is because it carries its own `.git`, so
	// the fixture must too — the scan detects the property, not the name.
	// `git worktree add` writes a gitdir POINTER FILE; a clone writes a
	// directory. Both shapes appear below so neither can regress alone.
	markCheckoutDir := func(rel string) {
		full := filepath.Join(root, rel, ".git")
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
	}
	markCheckoutFile := func(rel string) {
		full := filepath.Join(root, rel, ".git")
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	// One real offender in the tree's own source, and the same file inside
	// nested checkouts under BOTH worktree conventions, plus the other
	// excluded directories.
	write(filepath.Join("internal", "real", "parser.go"))
	write(filepath.Join(".worktrees", "issue-465", "internal", "real", "parser.go"))
	write(filepath.Join(".worktrees", "issue-467", "internal", "execution", "worktree_sweep.go"))
	// #1200: the agent worktree convention. Before the property-based skip
	// this path was scanned, and the whole-tree race gate failed for anyone
	// holding an agent worktree.
	write(filepath.Join(".claude", "worktrees", "issue-1150", "internal", "execution", "worktree_sweep.go"))
	write(filepath.Join(".claude", "worktrees", "issue-1150", "internal", "cmd", "batchfailures", "extractor.go"))
	write(filepath.Join("node_modules", "dep", "parser.go"))
	write(filepath.Join("vendor", "dep", "parser.go"))

	markCheckoutDir(filepath.Join(".worktrees", "issue-465"))
	markCheckoutFile(filepath.Join(".worktrees", "issue-467"))
	markCheckoutFile(filepath.Join(".claude", "worktrees", "issue-1150"))

	offenders := scanForIssueParsers(t, root)

	if len(offenders) != 1 {
		t.Fatalf("got %d offenders, want exactly the one in the tree's own source:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
	if !strings.HasPrefix(offenders[0], filepath.Join("internal", "real", "parser.go")) {
		t.Errorf("offender = %q, want the one under internal/real/", offenders[0])
	}
	for _, o := range offenders {
		// Deliberately NOT `.worktrees`: that substring is blind to the
		// `.claude/worktrees/` convention, and an assertion that cannot fail
		// for a convention it does not name is how #1200 followed #851.
		if strings.Contains(o, "worktrees") {
			t.Errorf("a nested checkout was scanned: %q", o)
		}
	}
}
