package ipc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// bypassable names board/project constructors that must not appear in this
// package outside board_services.go.
//
// gh.NewBoardService returns a reader that does not consult the snapshot cache;
// gh.NewProjectService returns a writer that does not invalidate it; and
// state.NewBoardStateServiceForClient builds both of those internally. Each one
// is correct in a package that has no cache — which is why they still exist —
// and each one is a silent staleness bug HERE.
var bypassable = map[string]bool{
	"NewBoardService":               true,
	"NewProjectService":             true,
	"NewBoardStateServiceForClient": true,
}

// TestBoardServicesAreNotBypassed is a SOURCE-level test, and it is
// source-level on purpose (#848).
//
// The defect it pins cannot be seen from behaviour. Every board verb in this
// package used to construct its own service inline, so `board.list` read past
// the snapshot cache and the five mutating verbs wrote past its invalidation —
// and every existing test passed, because each supplied its own service and
// never asked which one production builds. That is **Unpinned Wiring**
// (docs/FAILURE_TAXONOMY.md): a guarantee produced by WHERE a thing is
// constructed, invisible to the mechanism's own tests.
//
// The cheaper fix — invalidate at each mutating call site — was rejected for
// exactly the reason this test exists: it works until someone adds a sixth
// verb, and nothing goes red when they do. Here, adding one goes red at once.
//
// board_services.go is the exemption because it IS the interception point.
// Test files are exempt: a test constructing a plain service to stand in for a
// wrapped one is doing the right thing.
func TestBoardServicesAreNotBypassed(t *testing.T) {
	root := packageDir(t)

	var offenders []string
	fset := token.NewFileSet()

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "board_services.go" {
			continue
		}
		path := filepath.Join(root, name)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this test cannot parse is not a finding about wiring.
			continue
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !bypassable[sel.Sel.Name] {
				return true
			}
			pos := fset.Position(call.Pos())
			offenders = append(offenders, name+":"+itoa(pos.Line)+" "+sel.Sel.Name)
			return true
		})
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("board/project service(s) constructed outside board_services.go, so they bypass the snapshot cache and its invalidation:\n  %s\n"+
			"Use s.boardServicesFor / s.boardStateFor instead. Adding the invalidation at the call site is the shape #848 rejected.",
			strings.Join(offenders, "\n  "))
	}

	// A guard against the guard. If this walk ever scans nothing — the package
	// is renamed, the files move, getwd changes meaning under a new test
	// runner — it would pass silently forever while asserting nothing.
	if scanned == 0 {
		t.Fatal("scanned no production files in this package — this test is asserting nothing")
	}
}

// TestBoardServicesPinWouldCatchABypass proves the pin can go red.
//
// A pin whose failure path has never run is indistinguishable from one whose
// matcher is broken. This feeds the same matcher a source file containing the
// bypass and asserts it is found, so the guard is known to be capable of
// failing rather than merely observed to be passing.
func TestBoardServicesPinWouldCatchABypass(t *testing.T) {
	const src = `package ipc

func handler(c *gh.Client) {
	svc := gh.NewBoardService(c, "o", 1)
	_ = svc
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && bypassable[sel.Sel.Name] {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("the matcher did not flag a direct gh.NewBoardService call — the pin cannot go red")
	}
}

func packageDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
