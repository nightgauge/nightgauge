package execution

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// TestRegistrationWaitsShareTheirTimeoutBudget is a structural guard for
// #1453: every wait in manager_test.go for "a fact a just-spawned subprocess
// publishes that this process does not observe directly" must go through the
// file's shared pollUntil helper, and every such call must share one budget
// (30s, the convention already established at the #555 call sites). Without
// this check, a future test can reintroduce a hand-rolled deadline/poll loop
// with its own ad-hoc, narrower timeout — exactly what #1453 found at the
// #564 test's registration wait (5s against the file's own 30s convention) —
// and nothing would catch it.
//
// This parses manager_test.go's own AST rather than asserting on timing
// behavior directly: the risk #1453 describes (a flake under a saturated CI
// runner) is not deterministically reproducible, but the inconsistent-budget
// defect it stems from is a static property of the source and can be checked
// exactly.
func TestRegistrationWaitsShareTheirTimeoutBudget(t *testing.T) {
	const wantBudgetSeconds = 30

	src, err := os.ReadFile("manager_test.go")
	if err != nil {
		t.Fatalf("reading manager_test.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "manager_test.go", src, 0)
	if err != nil {
		t.Fatalf("parsing manager_test.go: %v", err)
	}

	var handRolled []string
	var offBudget []string

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name.Name == "pollUntil" {
			continue
		}
		fnName := fd.Name.Name
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				// Matches `deadline := time.Now().Add(<budget>)`, the exact
				// shape pollUntil itself uses — a hand-rolled duplicate of it
				// anywhere else is the #1453 defect regardless of budget.
				if isTimeNowAddAssign(stmt) {
					handRolled = append(handRolled, fnName)
				}
			case *ast.CallExpr:
				ident, ok := stmt.Fun.(*ast.Ident)
				if !ok || ident.Name != "pollUntil" || len(stmt.Args) != 4 {
					return true
				}
				seconds, ok := secondsLiteral(stmt.Args[2])
				if !ok {
					t.Errorf("%s: pollUntil timeout argument is not a literal `<N> * time.Second` this check can evaluate", fnName)
					return true
				}
				if seconds != wantBudgetSeconds {
					offBudget = append(offBudget, fnName)
				}
			}
			return true
		})
	}

	if len(handRolled) > 0 {
		t.Errorf("hand-rolled deadline/poll loop(s) found in %v — wait for a fact via the shared "+
			"pollUntil helper instead of duplicating its logic inline (#1453)", handRolled)
	}
	if len(offBudget) > 0 {
		t.Errorf("pollUntil called with a timeout other than %ds in %v — every wait in this file for "+
			"a just-published subprocess fact should share one budget (#1453)", wantBudgetSeconds, offBudget)
	}
}

// isTimeNowAddAssign reports whether stmt is `<name> := time.Now().Add(<x>)`.
func isTimeNowAddAssign(stmt *ast.AssignStmt) bool {
	if len(stmt.Rhs) != 1 {
		return false
	}
	call, ok := stmt.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	addSel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || addSel.Sel.Name != "Add" {
		return false
	}
	nowCall, ok := addSel.X.(*ast.CallExpr)
	if !ok || len(nowCall.Args) != 0 {
		return false
	}
	nowSel, ok := nowCall.Fun.(*ast.SelectorExpr)
	if !ok || nowSel.Sel.Name != "Now" {
		return false
	}
	pkg, ok := nowSel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}

// secondsLiteral evaluates an expression of the exact shape
// `<int literal> * time.Second`, returning the integer count of seconds.
func secondsLiteral(expr ast.Expr) (int64, bool) {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op != token.MUL {
		return 0, false
	}
	lit, ok := bin.X.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	sel, ok := bin.Y.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Second" {
		return 0, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return 0, false
	}
	var n int64
	for _, c := range lit.Value {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}
