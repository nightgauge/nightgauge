package ipc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

// TestEveryHarnessSubtestBindsItsHarness keeps #2380 fixed: a subtest that
// uses an ipcTestHarness its enclosing test created must call h.bind(t) as its
// first statement. Unbound, a harness read failure inside the subtest runs
// FailNow on the parent test from the subtest's goroutine, and Go reports
// "subtest may have called FailNow on a parent test" in place of the
// harness's own message (#2367).
//
// It reads every _test.go file in this package. A harness is any variable or
// parameter of type *ipcTestHarness: declared as such, or assigned from a
// function whose result in that position is one. A subtest is the function
// literal passed to t.Run; it uses a harness when it names one declared
// outside the literal.
func TestEveryHarnessSubtestBindsItsHarness(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var parsed []*ast.File
	for _, name := range files {
		// Object resolution (the default) links each identifier to its
		// declaration within the file, which is all this check needs.
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("HARNESS ERROR: parse %s: %v", name, err)
		}
		parsed = append(parsed, f)
	}

	// The functions that return a harness, and at which result position.
	constructors := map[string]int{}
	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Type.Results == nil {
				continue
			}
			pos := 0
			for _, field := range fn.Type.Results.List {
				n := max(len(field.Names), 1)
				if isHarnessType(field.Type) {
					constructors[fn.Name.Name] = pos
				}
				pos += n
			}
		}
	}
	if _, ok := constructors["newIpcTestHarness"]; !ok {
		t.Fatal("HARNESS ERROR: newIpcTestHarness was not found; the check is not reading the harness")
	}

	var unbound []string
	subtests := 0
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isRunCall(call) {
				return true
			}
			lit, ok := call.Args[1].(*ast.FuncLit)
			if !ok {
				return true
			}
			used := harnessesUsedFrom(lit, constructors)
			if len(used) == 0 {
				return true
			}
			subtests++
			bound := boundHarnesses(lit)
			for _, name := range used {
				if !bound[name] {
					unbound = append(unbound, fset.Position(lit.Pos()).String()+": "+name)
				}
			}
			return true
		})
	}
	if subtests == 0 {
		t.Fatal("HARNESS ERROR: no subtest uses a harness; the check is not finding them")
	}
	sort.Strings(unbound)
	for _, u := range unbound {
		t.Errorf("%s.bind(t) is not the subtest's first statement (#2380)", u)
	}
}

// isHarnessType reports whether expr is *ipcTestHarness.
func isHarnessType(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "ipcTestHarness"
}

// isRunCall reports whether call is X.Run(name, func...) with two arguments.
func isRunCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Run" && len(call.Args) == 2
}

// harnessesUsedFrom names the harnesses lit refers to that are declared
// outside it, sorted.
func harnessesUsedFrom(lit *ast.FuncLit, constructors map[string]int) []string {
	seen := map[string]bool{}
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Obj == nil || id.Obj.Kind != ast.Var {
			return true
		}
		decl, ok := id.Obj.Decl.(ast.Node)
		if !ok || (decl.Pos() >= lit.Pos() && decl.End() <= lit.End()) {
			return true
		}
		if declaresHarness(id.Obj.Name, id.Obj.Decl, constructors) {
			seen[id.Name] = true
		}
		return true
	})
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// declaresHarness reports whether decl declares name as a harness.
func declaresHarness(name string, decl any, constructors map[string]int) bool {
	switch d := decl.(type) {
	case *ast.Field:
		return isHarnessType(d.Type)
	case *ast.ValueSpec:
		if d.Type != nil {
			return isHarnessType(d.Type)
		}
		for i, n := range d.Names {
			if n.Name == name && len(d.Values) == 1 {
				return harnessAt(d.Values[0], i, constructors)
			}
		}
	case *ast.AssignStmt:
		for i, lhs := range d.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name {
				continue
			}
			if len(d.Rhs) == 1 {
				return harnessAt(d.Rhs[0], i, constructors)
			}
			if i < len(d.Rhs) {
				return harnessAt(d.Rhs[i], 0, constructors)
			}
		}
	}
	return false
}

// harnessAt reports whether result index of expr is a harness: a call to a
// constructor whose result at index is one.
func harnessAt(expr ast.Expr, index int, constructors map[string]int) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	pos, ok := constructors[id.Name]
	return ok && pos == index
}

// boundHarnesses names the harnesses lit binds in its leading statements:
// each statement from the first that is X.bind(t), where t is lit's own
// parameter.
func boundHarnesses(lit *ast.FuncLit) map[string]bool {
	bound := map[string]bool{}
	params := lit.Type.Params.List
	if len(params) != 1 || len(params[0].Names) != 1 {
		return bound
	}
	param := params[0].Names[0].Name
	for _, stmt := range lit.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			break
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			break
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "bind" {
			break
		}
		recv, ok := sel.X.(*ast.Ident)
		arg, argOK := call.Args[0].(*ast.Ident)
		if !ok || !argOK || arg.Name != param {
			break
		}
		bound[recv.Name] = true
	}
	return bound
}
