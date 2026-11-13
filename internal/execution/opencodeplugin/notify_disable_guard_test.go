package opencodeplugin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRealHookBinariesDisableNotifications guards #3137: every real hook
// binary these tests spawn must carry NIGHTGAUGE_NOTIFY_DISABLE=1. Env lists
// built from scratch must go through hookBinEnv; env lists derived from
// os.Environ() inherit the value TestMain sets.
func TestRealHookBinariesDisableNotifications(t *testing.T) {
	if got := os.Getenv(notifyDisableEnv); got != "1" {
		t.Errorf("TestMain must set %s=1 for os.Environ()-derived harnesses, got %q", notifyDisableEnv, got)
	}

	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("HARNESS ERROR: could not list test files (err=%v, n=%d); the literal arm did not run", err, len(files))
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("HARNESS ERROR: could not parse %s: %v; the literal arm did not run", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "hookBinEnv" && fn.Recv == nil {
				return false
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && strings.HasPrefix(v, "NIGHTGAUGE_BIN"+"=") {
				t.Errorf("%s: set the hook binary with hookBinEnv(...), not a %q literal, "+
					"so the spawned build cannot post a desktop notification", fset.Position(lit.Pos()), v)
			}
			return true
		})
	}
}
