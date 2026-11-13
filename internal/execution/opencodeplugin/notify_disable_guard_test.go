package opencodeplugin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// hookBinKey is split so this file's own literals do not trip the guard.
const hookBinKey = "NIGHTGAUGE_" + "BIN"

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
		t.Fatalf("HARNESS ERROR: could not list test files (err=%v, n=%d); the guard did not run", err, len(files))
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("HARNESS ERROR: could not parse %s: %v; the guard did not run", name, err)
		}
		for _, v := range hookBinViolations(fset, f) {
			t.Errorf("%s; set the hook binary with hookBinEnv or withHookBin "+
				"so the spawned build cannot post a desktop notification", v)
		}
	}
}

// hookBinViolations reports every place outside func hookBinEnv that names
// the hook binary: a string literal prefixed "NIGHTGAUGE_BIN=" (plain or the
// left operand of a concatenation), or the bare key passed as a call argument
// to anything but removeEnv (upsertEnv, os.Setenv, t.Setenv, #3250).
func hookBinViolations(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Name.Name == "hookBinEnv" && n.Recv == nil {
				return false
			}
		case *ast.CallExpr:
			if calleeName(n.Fun) == "removeEnv" {
				return true
			}
			for _, arg := range n.Args {
				if v, ok := stringLit(arg); ok && v == hookBinKey {
					out = append(out, fmt.Sprintf("%s: %s passes %q as a key",
						fset.Position(arg.Pos()), calleeName(n.Fun), v))
				}
			}
		case *ast.BasicLit:
			if v, ok := stringLit(n); ok && strings.HasPrefix(v, hookBinKey+"=") {
				out = append(out, fmt.Sprintf("%s: %q literal", fset.Position(n.Pos()), v))
			}
		}
		return true
	})
	return out
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

// TestHookBinGuardReportsEachForm proves each arm of hookBinViolations can go
// red, and that the hookBinEnv route and removeEnv stay allowed (#3250).
func TestHookBinGuardReportsEachForm(t *testing.T) {
	key := hookBinKey
	cases := []struct {
		name string
		body string
		want int
	}{
		{"literal", `env = append(env, "` + key + `=/x")`, 1},
		{"concatenation", `env = append(env, "` + key + `="+bin)`, 1},
		{"upsertEnv key", `env = upsertEnv(env, "` + key + `", bin)`, 1},
		{"t.Setenv key", `t.Setenv("` + key + `", bin)`, 1},
		{"hookBinEnv route", `env = append(env, hookBinEnv(bin)...)`, 0},
		{"removeEnv", `env = removeEnv(env, "` + key + `")`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\nfunc hookBinEnv(bin string) []string {\n\treturn []string{\"" +
				key + "=\" + bin}\n}\n\nfunc f(t T, env []string, bin string) {\n\t" + tc.body + "\n}\n"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "snippet.go", src, 0)
			if err != nil {
				t.Fatalf("HARNESS ERROR: could not parse snippet: %v", err)
			}
			if got := hookBinViolations(fset, f); len(got) != tc.want {
				t.Errorf("violations = %d %v, want %d", len(got), got, tc.want)
			}
		})
	}
}
