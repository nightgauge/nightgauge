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
//
// The hand-rolled check follows the contract, not one spelling of it: a
// "hand-rolled poll loop" is any `for` loop whose body (or condition) reads a
// deadline value — one produced by time.Now().Add(...) or time.After(...),
// however that value reaches the loop (an inline call, or a variable
// assigned earlier in the same function). Respelling the deadline
// (time.After instead of time.Now().Add, or vice versa) does not escape it,
// and a plain fixture timestamp that is never read inside a `for` does not
// trip it.
//
// The budget check evaluates the timeout argument as a duration expression —
// `<int> * time.Second`, `time.Second * <int>`, `time.Minute / <int>`, or a
// package-level const built from those — rather than one fixed literal
// shape, so factoring the shared budget into a named constant does not make
// this check red. An argument it genuinely cannot evaluate (a computed
// expression, a variable of unknown origin) is skipped rather than treated
// as a violation: a check that cannot read an expression has no basis to
// assert the code is wrong.
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

	fileConsts := collectSecondsConsts(file)

	var handRolled []string
	var offBudget []string

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name.Name == "pollUntil" {
			continue
		}
		fnName := fd.Name.Name

		if hasHandRolledDeadlineLoop(fd.Body) {
			handRolled = append(handRolled, fnName)
		}

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "pollUntil" || len(call.Args) != 4 {
				return true
			}
			seconds, ok := durationSeconds(call.Args[2], fileConsts)
			if !ok {
				// The argument isn't one of the duration shapes this check
				// can evaluate (e.g. a computed expression) — that is not
				// evidence of an inconsistent budget, so there is nothing to
				// assert here.
				return true
			}
			if seconds != wantBudgetSeconds {
				offBudget = append(offBudget, fnName)
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

// hasHandRolledDeadlineLoop reports whether body contains a `for` loop that
// reads a deadline value — the #1453 shape, whatever variable name or call
// form produced that value. It first collects every identifier in body that
// is assigned directly from a deadline call (isDeadlineCall), then reports
// true if any `for` loop's subtree either references one of those
// identifiers or contains a deadline call inline (e.g. a `select` on
// `<-time.After(...)` written directly inside the loop, with no intervening
// variable).
func hasHandRolledDeadlineLoop(body *ast.BlockStmt) bool {
	deadlineIdents := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !isDeadlineCall(call) {
			return true
		}
		if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
			deadlineIdents[ident.Name] = true
		}
		return true
	})

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		forStmt, ok := n.(*ast.ForStmt)
		if !ok {
			return true
		}
		ast.Inspect(forStmt, func(inner ast.Node) bool {
			switch v := inner.(type) {
			case *ast.CallExpr:
				if isDeadlineCall(v) {
					found = true
				}
			case *ast.Ident:
				if deadlineIdents[v.Name] {
					found = true
				}
			}
			return true
		})
		return true
	})
	return found
}

// isDeadlineCall reports whether call is time.Now().Add(<duration>) or
// time.After(<duration>) — the two standard-library shapes that produce a
// point in time (or a channel that fires at one) against which elapsed time
// is later checked. pollUntil's own body uses time.Now().Add exactly once,
// centrally; a `for` loop anywhere else in the file that reads a value
// produced by either shape is a hand-rolled duplicate of pollUntil's own
// polling logic, regardless of which of the two shapes it uses.
func isDeadlineCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "After" {
		return true
	}
	if sel.Sel.Name != "Add" || len(call.Args) != 1 {
		return false
	}
	nowCall, ok := sel.X.(*ast.CallExpr)
	if !ok || len(nowCall.Args) != 0 {
		return false
	}
	nowSel, ok := nowCall.Fun.(*ast.SelectorExpr)
	if !ok || nowSel.Sel.Name != "Now" {
		return false
	}
	nowPkg, ok := nowSel.X.(*ast.Ident)
	return ok && nowPkg.Name == "time"
}

// collectSecondsConsts finds every package-level const in file whose value
// is a duration expression durationSeconds can evaluate, and returns each
// one's value in seconds. This lets the budget check accept a named
// constant (e.g. `const registrationWaitBudget = 30 * time.Second`) at a
// pollUntil call site, not only an inline literal.
func collectSecondsConsts(file *ast.File) map[string]int64 {
	consts := map[string]int64{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if seconds, ok := durationSeconds(vs.Values[i], consts); ok {
					consts[name.Name] = seconds
				}
			}
		}
	}
	return consts
}

// durationSeconds evaluates expr as a compile-time duration expression,
// returning its value in whole seconds. It understands a time unit selector
// (time.Second, time.Minute, time.Hour), an integer literal multiplied by
// one of those (in either operand order) or by an already-resolved duration,
// division of a duration by an integer literal (only when it divides evenly),
// and a reference to a package-level const already recorded in consts. Any
// other shape — a function call, a variable of unknown origin, non-integer
// division — reports false rather than guessing.
func durationSeconds(expr ast.Expr, consts map[string]int64) (int64, bool) {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return unitSeconds(e)
	case *ast.Ident:
		seconds, ok := consts[e.Name]
		return seconds, ok
	case *ast.BinaryExpr:
		switch e.Op {
		case token.MUL:
			if n, ok := intLiteral(e.X); ok {
				if s, ok := durationSeconds(e.Y, consts); ok {
					return n * s, true
				}
			}
			if n, ok := intLiteral(e.Y); ok {
				if s, ok := durationSeconds(e.X, consts); ok {
					return n * s, true
				}
			}
			return 0, false
		case token.QUO:
			if n, ok := intLiteral(e.Y); ok && n != 0 {
				if s, ok := durationSeconds(e.X, consts); ok && s%n == 0 {
					return s / n, true
				}
			}
			return 0, false
		}
	}
	return 0, false
}

// unitSeconds reports the number of seconds in one of the standard duration
// unit selectors, e.g. time.Minute reports 60.
func unitSeconds(sel *ast.SelectorExpr) (int64, bool) {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return 0, false
	}
	switch sel.Sel.Name {
	case "Second":
		return 1, true
	case "Minute":
		return 60, true
	case "Hour":
		return 3600, true
	}
	return 0, false
}

// intLiteral evaluates expr as a non-negative decimal integer literal.
func intLiteral(expr ast.Expr) (int64, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
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
