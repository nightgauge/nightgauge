package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// TestEveryDependencyReaderGetsTheWorkspaceAliasMap pins #2349's first
// acceptance criterion at the call sites, where the regression would happen:
// every call in this binary that parses body-declared dependencies — directly
// or through a helper that forwards the map — passes an alias map built by
// workspaceRepoAliases / launchRepoAliases, never nil and never a hand-made
// map. Before #2349 every one of them passed nil, and the helpers' own tests
// cannot see a call site that reverts: the review found that handing nil to
// `hook check-deps`, or to `serve`, left the whole suite green.
//
// An argument passes when it is a call to one of the two builders, or the
// forwarded parameter `repoAliases` of a function that is itself checked here.
func TestEveryDependencyReaderGetsTheWorkspaceAliasMap(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["main"]
	if !ok {
		t.Fatalf("package main not found (got %v)", pkgs)
	}

	// The alias argument's position in each function that takes the map.
	aliasArg := map[string]int{
		"hooks.EvaluateIssueDeps":             5,
		"orchestrator.NewAutonomousScheduler": 3,
		"depgraph.BuildGraph":                 3,
		"depgraph.BuildGraphWithBoards":       4,
		"depgraph.BuildGraphFromItems":        3,
		"depgraph.ParseDependencyRefs":        2,
		"depgraph.ParseCrossRepoRefs":         1,
	}
	// …and every function of this package that forwards it, as a parameter
	// named repoAliases.
	for _, f := range pkg.Files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			i := 0
			for _, field := range fd.Type.Params.List {
				if len(field.Names) == 0 {
					i++
					continue
				}
				for _, name := range field.Names {
					if name.Name == "repoAliases" {
						aliasArg[fd.Name.Name] = i
					}
					i++
				}
			}
		}
	}
	for _, helper := range []string{"evaluateDepsGate", "depsGatePromoteSweep", "checkPRMergeBlockers"} {
		if _, ok := aliasArg[helper]; !ok {
			t.Errorf("%s no longer takes a repoAliases parameter; update this pin", helper)
		}
	}

	calleeName := func(call *ast.CallExpr) string {
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok {
				return x.Name + "." + fn.Sel.Name
			}
		}
		return ""
	}
	builtByTheWorkspace := func(arg ast.Expr) bool {
		switch a := arg.(type) {
		case *ast.CallExpr:
			name := calleeName(a)
			return name == "workspaceRepoAliases" || name == "launchRepoAliases"
		case *ast.Ident:
			return a.Name == "repoAliases"
		}
		return false
	}

	checked := map[string]int{}
	for path, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call)
			idx, ok := aliasArg[name]
			if !ok {
				return true
			}
			if idx >= len(call.Args) {
				t.Errorf("%s: %s called with %d arguments, want the alias map at %d",
					fset.Position(call.Pos()), name, len(call.Args), idx)
				return true
			}
			checked[name]++
			if arg := call.Args[idx]; !builtByTheWorkspace(arg) {
				t.Errorf("%s (%s): %s gets alias map %s; pass workspaceRepoAliases(...) or "+
					"launchRepoAliases(...) (#2349)", fset.Position(arg.Pos()), path, name, exprString(fset, arg))
			}
			return true
		})
	}

	// The production readers this pin knows of. A rename that hides one from
	// the walk fails here instead of passing quietly.
	for name, min := range map[string]int{
		"hooks.EvaluateIssueDeps":             3, // hook check-deps, deps-gate, the PR-merge guard
		"orchestrator.NewAutonomousScheduler": 2, // serve, autonomous
		"depgraph.BuildGraph":                 2, // graph build, next
		"evaluateDepsGate":                    1,
		"depsGatePromoteSweep":                1,
		"checkPRMergeBlockers":                1,
	} {
		if checked[name] < min {
			t.Errorf("found %d call(s) of %s, want at least %d; update this pin", checked[name], name, min)
		}
	}
}

// exprString renders an expression for a failure message.
func exprString(fset *token.FileSet, e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.CallExpr:
		if s, ok := v.Fun.(*ast.Ident); ok {
			return s.Name + "(…)"
		}
	}
	return fset.Position(e.Pos()).String()
}
