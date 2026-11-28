package stages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestACExprStringMatchesTypesExprString pins acExprString to the
// go/types printer it replaces (#3675), for every ast.Expr kind.
func TestACExprStringMatchesTypesExprString(t *testing.T) {
	sources := []string{
		"x", "a.b.c", "f(xs...)", "f(a, b)", "f()",
		"42", "3.14", "2i", "'x'", `"s"`, "`raw`",
		"func(a int) (b string) { return }", "func() {}",
		"T{}", "T{1, 2}", "[]int{1}", "map[string]int{}",
		"(a + b)", "m[k]", "G[int, string]", "G[int]",
		"s[1:2]", "s[:]", "s[1:]", "s[:2]", "s[1:2:3]", "s[:2:3]",
		"x.(T)", "x.(*pkg.T)", "*p", "-x", "!ok", "<-ch", "&T{}", "^x",
		"a + b*c", "a == b && c != d", "a << 2",
		"[]int", "[4]byte", "[...]int{1}", "[N]T",
		"struct{}", "struct{ a, b int; c string `json:\"c\"`; io.Reader }",
		"func()", "func(int, string) error", "func(a, b int) (int, error)",
		"func(...int)", "func(xs ...string) (n int)",
		"interface{}", "interface{ M(int) string; io.Reader }",
		"interface{ ~int | string }", "interface{ comparable; N() }",
		"map[string][]int", "chan int", "chan<- int", "<-chan int",
		"chan (<-chan int)", "func(T) G[map[K]V, chan<- E]",
	}
	exprs := map[string]ast.Expr{
		"BadExpr":      &ast.BadExpr{},
		"KeyValueExpr": &ast.KeyValueExpr{Key: ast.NewIdent("k"), Value: ast.NewIdent("v")},
		"nil":          nil,
		"Ellipsis":     &ast.Ellipsis{Elt: ast.NewIdent("int")},
		"BareEllipsis": &ast.Ellipsis{},
		"FuncTypeTP": &ast.FuncType{
			TypeParams: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("T")}, Type: ast.NewIdent("any")}}},
			Params:     &ast.FieldList{},
		},
	}
	for _, src := range sources {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", src, err)
		}
		exprs[src] = e
	}
	for name, e := range exprs {
		if got, want := acExprString(e), types.ExprString(e); got != want {
			t.Errorf("%s: acExprString = %q, types.ExprString = %q", name, got, want)
		}
	}
}

// TestStagesProductionCodeDoesNotImportGoTypes keeps go/types out of the
// release CLI's link set (#3675): only _test.go files may import it.
func TestStagesProductionCodeDoesNotImportGoTypes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("HARNESS ERROR: no Go files found: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "go/types" {
				t.Errorf("%s imports go/types; use acExprString instead", name)
			}
		}
	}
}
