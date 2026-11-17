package orchestrator

// Policy for #3144: every top-level Test in this package runs in parallel
// unless it reaches state that parallel tests would share.
//
// Go runs one package's top-level tests one after another unless they call
// t.Parallel(), and this package was the slowest one in `go test ./...`. A test
// MUST call t.Parallel() as its first statement unless it is serial-required:
// its body, or any same-package func it reaches (called or referenced, through
// any number of same-package funcs), does one of these:
//
//  1. sets the environment, the working directory or the standard logger
//     (t.Setenv, t.Chdir, os.Setenv, os.Chdir, log.SetOutput, log.SetFlags);
//  2. assigns to a package-level var, of this package or another one
//     (`reconcileExecGh = stub`, `os.Stdout = w`, `registry[k] = v`).
//
// A third case, a test that asserts on wall-clock timing or uses a fixed path
// or port, cannot be detected here; its author keeps it serial (#2950).
//
// A serial test says why, either in its doc comment (`// Serial: <reason>`) or
// once for the whole file in a top-level comment before its first Test
// (`// Serial (file): <reason>`).
//
// The walk follows calls and references that resolve to a same-package func
// or method. A hazard reached only through an interface method, a func-typed
// field or a package-level func var is not followed.
//
// The parallel phase of a test binary starts only after every serial
// top-level test has finished, so a parallel test may read a package var that
// a serial test swaps: the two never overlap. Another package is seen only
// through the helpers parallelEnvCalls lists; the `-race -count=N` runs of
// the package are the backstop for any other state hidden there.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// parallelEnvCalls are the package-qualified calls that change process-wide
// state: the environment, the working directory, or the standard logger.
var parallelEnvCalls = map[string]map[string]bool{
	"os":       {"Setenv": true, "Unsetenv": true, "Clearenv": true, "Chdir": true},
	"syscall":  {"Setenv": true, "Unsetenv": true, "Clearenv": true, "Chdir": true},
	"log":      {"SetOutput": true, "SetFlags": true, "SetPrefix": true},
	"log/slog": {"SetDefault": true, "SetLogLoggerLevel": true},
	// Helpers in other packages of this module that call t.Setenv. The check
	// does not read other packages, so a new one is listed here; testing
	// panics when a parallel test reaches one, so the miss is loud.
	"github.com/nightgauge/nightgauge/internal/github/githubtest": {"New": true},
}

// parallelEnvMethods are the testing.T/B methods t.Parallel() cannot be
// combined with; testing panics when a parallel test calls one.
var parallelEnvMethods = map[string]bool{"Setenv": true, "Chdir": true}

// moduleMajorVersionElem matches a module path's major-version element (v2).
var moduleMajorVersionElem = regexp.MustCompile(`^v[0-9]+$`)

// The two reason markers a serial test may carry.
const (
	serialFileMarker = "Serial (file):"
	serialFuncMarker = "Serial:"
)

// minPackageFiles is fewer files than this package has; parsing fewer means
// the test did not run in the package directory and would check nothing.
const minPackageFiles = 100

// stubImporter hands go/types an empty package for every import. The check
// needs only to resolve this package's own identifiers (package vars, funcs,
// methods, local shadowing) and the package an imported name refers to, so
// type-checking the dependencies would only cost time.
type stubImporter struct{ pkgs map[string]*types.Package }

func (s stubImporter) Import(importPath string) (*types.Package, error) {
	if p, ok := s.pkgs[importPath]; ok {
		return p, nil
	}
	name := path.Base(importPath)
	if moduleMajorVersionElem.MatchString(name) {
		name = path.Base(path.Dir(importPath))
	}
	// A guess: a wrong name only leaves that import's selectors unresolved,
	// and the check needs the right name only for the packages it names.
	if i := strings.IndexAny(name, ".-"); i > 0 {
		name = name[:i]
	}
	p := types.NewPackage(importPath, name)
	p.MarkComplete()
	s.pkgs[importPath] = p
	return p, nil
}

// parallelFunc is one same-package func or method and what its body does.
type parallelFunc struct {
	decl    *ast.FuncDecl
	hazard  string        // first direct hazard, "" when none
	callees []*types.Func // same-package funcs it calls or references
}

type parallelChecker struct {
	fset  *token.FileSet
	info  *types.Info
	pkg   *types.Package
	funcs map[*types.Func]*parallelFunc
	memo  map[*types.Func]string
	busy  map[*types.Func]bool
}

func newParallelChecker(fset *token.FileSet, files []*ast.File) *parallelChecker {
	info := &types.Info{
		Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		Importer: stubImporter{pkgs: map[string]*types.Package{}},
		// Stub imports leave every selector into them unresolved, so errors
		// are expected and ignored. The cost: an error in this package's own
		// code could also hide a hazard. The tree compiles before this test
		// runs, so that case is a fixture-only concern.
		Error: func(error) {},
	}
	pkg, _ := conf.Check("orchestrator", fset, files, info)
	c := &parallelChecker{
		fset:  fset,
		info:  info,
		pkg:   pkg,
		funcs: map[*types.Func]*parallelFunc{},
		memo:  map[*types.Func]string{},
		busy:  map[*types.Func]bool{},
	}
	for _, file := range files {
		for _, d := range file.Decls {
			decl, ok := d.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				continue
			}
			obj, ok := info.Defs[decl.Name].(*types.Func)
			if !ok {
				continue
			}
			c.funcs[obj] = c.scan(decl)
		}
	}
	return c
}

func (c *parallelChecker) at(n ast.Node) string {
	p := c.fset.Position(n.Pos())
	return fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line)
}

// importedPkg reports the import path an identifier names, or "".
func (c *parallelChecker) importedPkg(e ast.Expr) string {
	id, ok := e.(*ast.Ident)
	if !ok {
		return ""
	}
	if pn, ok := c.info.Uses[id].(*types.PkgName); ok {
		return pn.Imported().Path()
	}
	return ""
}

// sharedVar names the package-level var an assignment target writes, or "".
func (c *parallelChecker) sharedVar(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.SelectorExpr:
			if p := c.importedPkg(x.X); p != "" {
				return p + "." + x.Sel.Name
			}
			e = x.X
		case *ast.Ident:
			v, ok := c.info.Uses[x].(*types.Var)
			if ok && c.pkg != nil && v.Parent() == c.pkg.Scope() {
				return x.Name
			}
			return ""
		default:
			return ""
		}
	}
}

func (c *parallelChecker) scan(decl *ast.FuncDecl) *parallelFunc {
	f := &parallelFunc{decl: decl}
	seen := map[*types.Func]bool{}
	note := func(n ast.Node, what string) {
		if f.hazard == "" {
			f.hazard = what + " at " + c.at(n)
		}
	}
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				return true
			}
			for _, lhs := range x.Lhs {
				if v := c.sharedVar(lhs); v != "" {
					note(x, "assigns package var "+v)
				}
			}
		case *ast.IncDecStmt:
			if v := c.sharedVar(x.X); v != "" {
				note(x, "assigns package var "+v)
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "delete" || id.Name == "clear") {
				if _, builtin := c.info.Uses[id].(*types.Builtin); builtin && len(x.Args) > 0 {
					if v := c.sharedVar(x.Args[0]); v != "" {
						note(x, id.Name+"s from package var "+v)
					}
				}
			}
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if p := c.importedPkg(sel.X); p != "" {
				if parallelEnvCalls[p][sel.Sel.Name] {
					note(x, "calls "+p+"."+sel.Sel.Name)
				}
			} else if parallelEnvMethods[sel.Sel.Name] {
				note(x, "calls ."+sel.Sel.Name)
			}
		case *ast.Ident:
			fn, ok := c.info.Uses[x].(*types.Func)
			if ok && c.pkg != nil && fn.Pkg() == c.pkg && !seen[fn] {
				seen[fn] = true
				f.callees = append(f.callees, fn)
			}
		}
		return true
	})
	return f
}

// hazard returns the first serial-required hazard fn reaches, with the path of
// same-package funcs that leads to it, or "" when it reaches none.
func (c *parallelChecker) hazard(fn *types.Func) string {
	if h, ok := c.memo[fn]; ok {
		return h
	}
	f := c.funcs[fn]
	if f == nil || c.busy[fn] {
		return "" // no body here, or a cycle already being walked
	}
	c.busy[fn] = true
	h := f.hazard
	for _, callee := range f.callees {
		if h != "" {
			break
		}
		if sub := c.hazard(callee); sub != "" {
			h = callee.Name() + " → " + sub
		}
	}
	c.busy[fn] = false
	c.memo[fn] = h
	return h
}

// testParam returns the name of a Test func's *testing.T parameter, or "".
func testParam(decl *ast.FuncDecl) string {
	if decl.Recv != nil || !strings.HasPrefix(decl.Name.Name, "Test") || decl.Name.Name == "TestMain" {
		return ""
	}
	params := decl.Type.Params.List
	if len(params) != 1 || len(params[0].Names) != 1 {
		return ""
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "testing" {
		return ""
	}
	return params[0].Names[0].Name
}

// isParallelCall reports whether stmt is `<t>.Parallel()`.
func isParallelCall(stmt ast.Stmt, t string) bool {
	es, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == t
}

// serialMarker returns the reason marker a comment group carries with a
// non-empty reason after it, and whether it carries one.
func serialMarker(groups ...*ast.CommentGroup) (string, bool) {
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, c := range g.List {
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			for _, prefix := range []string{serialFileMarker, serialFuncMarker} {
				if r, ok := strings.CutPrefix(text, prefix); ok && strings.TrimSpace(r) != "" {
					return prefix, true
				}
			}
		}
	}
	return "", false
}

// fileSerialReason reports whether file carries a `// Serial (file):` comment
// before its first Test func.
func fileSerialReason(file *ast.File) bool {
	first := file.End()
	for _, d := range file.Decls {
		if decl, ok := d.(*ast.FuncDecl); ok && testParam(decl) != "" {
			first = decl.Pos()
			break
		}
	}
	for _, g := range file.Comments {
		if g.Pos() >= first {
			break
		}
		if prefix, ok := serialMarker(g); ok && prefix == serialFileMarker {
			return true
		}
	}
	return false
}

// parallelViolations applies the policy to every Test func in the test files
// (named by testFiles) and returns one line per violation, sorted.
func parallelViolations(fset *token.FileSet, files []*ast.File, testFiles map[*ast.File]bool) []string {
	c := newParallelChecker(fset, files)
	var out []string
	for _, file := range files {
		if !testFiles[file] {
			continue
		}
		fileSerial := fileSerialReason(file)
		for _, d := range file.Decls {
			decl, ok := d.(*ast.FuncDecl)
			if !ok || decl.Body == nil {
				continue
			}
			t := testParam(decl)
			if t == "" {
				continue
			}
			parallel := len(decl.Body.List) > 0 && isParallelCall(decl.Body.List[0], t)
			for i, stmt := range decl.Body.List {
				if i > 0 && isParallelCall(stmt, t) {
					out = append(out, fmt.Sprintf("%s: %s calls %s.Parallel() after its first statement; make it the first",
						c.at(stmt), decl.Name.Name, t))
				}
			}
			fn, _ := c.info.Defs[decl.Name].(*types.Func)
			h := c.hazard(fn)
			_, docSerial := serialMarker(decl.Doc)
			switch {
			case parallel && h != "":
				out = append(out, fmt.Sprintf("%s: %s calls %s.Parallel() but %s; make it serial with a `// Serial:` reason",
					c.at(decl), decl.Name.Name, t, h))
			case parallel && (docSerial || fileSerial):
				out = append(out, fmt.Sprintf("%s: %s calls %s.Parallel() and also carries a Serial reason; drop one",
					c.at(decl), decl.Name.Name, t))
			case !parallel && !docSerial && !fileSerial && h != "":
				out = append(out, fmt.Sprintf("%s: %s is serial because %s; say so in a `// Serial:` doc comment line",
					c.at(decl), decl.Name.Name, h))
			case !parallel && !docSerial && !fileSerial:
				out = append(out, fmt.Sprintf("%s: %s neither calls %s.Parallel() first nor carries a `// Serial:` reason",
					c.at(decl), decl.Name.Name, t))
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestOrchestratorTestsFollowParallelPolicy(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	testFiles := map[*ast.File]bool{}
	for _, name := range names {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		testFiles[file] = strings.HasSuffix(name, "_test.go")
	}
	if len(testFiles) < minPackageFiles {
		t.Fatalf("parsed only %d files; is the working directory the package?", len(testFiles))
	}
	for _, v := range parallelViolations(fset, files, testFiles) {
		t.Error(v)
	}
}

// TestParallelPolicyFlagsHazards proves the check can fail: each hazard the
// policy names is caught in a parallel test, directly and through helpers, and
// a serial test without a reason is caught.
func TestParallelPolicyFlagsHazards(t *testing.T) {
	t.Parallel()
	const prod = `package orchestrator

var stubbed = func() int { return 1 }
var registry = map[string]int{}
var counter int

func lazyInit() { registry["k"] = 1 }
`
	const tests = `package orchestrator

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

func setEnvHelper(t *testing.T) { t.Setenv("A", "b") }
func indirect(t *testing.T)     { setEnvHelper(t) }
func swap(t *testing.T) {
	old := stubbed
	stubbed = func() int { return 2 }
	t.Cleanup(func() { stubbed = old })
}

func TestParallelSetenvViaHelpers(t *testing.T) { t.Parallel(); indirect(t) }
func TestParallelOsSetenv(t *testing.T)         { t.Parallel(); os.Setenv("A", "b") }
func TestParallelChdir(t *testing.T)            { t.Parallel(); t.Chdir(t.TempDir()) }
func TestParallelSwapsVar(t *testing.T)         { t.Parallel(); swap(t) }
func TestParallelReferencesSwap(t *testing.T)   { t.Parallel(); f := swap; f(t) }
func TestParallelStdout(t *testing.T)           { t.Parallel(); os.Stdout = nil }
func TestParallelProdWrite(t *testing.T)        { t.Parallel(); lazyInit() }
func TestParallelGithubtest(t *testing.T)      { t.Parallel(); githubtest.New(t, nil) }
func TestParallelLogOutput(t *testing.T)       { t.Parallel(); log.SetOutput(&bytes.Buffer{}) }
func TestParallelIncrements(t *testing.T)      { t.Parallel(); counter++ }
func TestParallelDeletes(t *testing.T)         { t.Parallel(); delete(registry, "k") }
func TestParallelSlog(t *testing.T)            { t.Parallel(); slog.SetDefault(nil) }
func TestParallelLate(t *testing.T)             { _ = 1; t.Parallel() }
func TestSerialNoReason(t *testing.T)           { swap(t) }
func TestSerialNoHazardNoReason(t *testing.T)   {}

// Serial: asserts on elapsed wall-clock time.
func TestSerialWithReason(t *testing.T) {}

// Serial: says it is serial but is not.
func TestParallelAndSerial(t *testing.T) { t.Parallel() }

// Serial:
func TestSerialEmptyReason(t *testing.T) {}

func TestParallelShadowedLocal(t *testing.T) {
	t.Parallel()
	stubbed := func() int { return 3 }
	stubbed = nil
	_ = stubbed
	_ = registry["k"]
}
`
	const fileSerial = `package orchestrator

import "testing"

// Serial (file): every test here swaps stubbed.

func TestFileSerialA(t *testing.T) { stubbed = nil }
func TestFileSerialB(t *testing.T) {}
`
	const lateFileSerial = `package orchestrator

import "testing"

func TestLateFirst(t *testing.T) { t.Parallel() }

// Serial (file): too late: it follows the first Test.

func TestLateMarker(t *testing.T) {}
`
	fset := token.NewFileSet()
	testFiles := map[*ast.File]bool{}
	var files []*ast.File
	for i, src := range []string{prod, tests, fileSerial, lateFileSerial} {
		file, err := parser.ParseFile(fset, fmt.Sprintf("f%d_test.go", i), src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		testFiles[file] = i > 0
	}
	got := strings.Join(parallelViolations(fset, files, testFiles), "\n")
	for _, want := range []string{
		"TestParallelSetenvViaHelpers calls t.Parallel() but indirect → setEnvHelper → calls .Setenv",
		"TestParallelOsSetenv calls t.Parallel() but calls os.Setenv",
		"TestParallelChdir calls t.Parallel() but calls .Chdir",
		"TestParallelSwapsVar calls t.Parallel() but swap → assigns package var stubbed",
		"TestParallelReferencesSwap calls t.Parallel() but swap → assigns package var stubbed",
		"TestParallelStdout calls t.Parallel() but assigns package var os.Stdout",
		"TestParallelProdWrite calls t.Parallel() but lazyInit → assigns package var registry",
		"TestParallelGithubtest calls t.Parallel() but calls github.com/nightgauge/nightgauge/internal/github/githubtest.New",
		"TestParallelLogOutput calls t.Parallel() but calls log.SetOutput",
		"TestParallelIncrements calls t.Parallel() but assigns package var counter",
		"TestParallelDeletes calls t.Parallel() but deletes from package var registry",
		"TestParallelSlog calls t.Parallel() but calls log/slog.SetDefault",
		"TestParallelAndSerial calls t.Parallel() and also carries a Serial reason",
		"TestSerialEmptyReason neither calls t.Parallel() first nor carries",
		"TestLateMarker neither calls t.Parallel() first nor carries",
		"TestParallelLate calls t.Parallel() after its first statement",
		"TestSerialNoReason is serial because swap → assigns package var stubbed",
		"TestSerialNoHazardNoReason neither calls t.Parallel() first nor carries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("violations do not contain %q; got:\n%s", want, got)
		}
	}
	for _, clean := range []string{"TestSerialWithReason", "TestParallelShadowedLocal", "TestFileSerialA", "TestFileSerialB"} {
		if strings.Contains(got, clean+" ") {
			t.Errorf("%s is compliant but was flagged:\n%s", clean, got)
		}
	}
}
