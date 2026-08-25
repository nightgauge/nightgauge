package execution

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

// TestEveryProductionSweepCallSiteOpensTheMergedPRDoor is a SOURCE-level test,
// and it is source-level on purpose (#916).
//
// `MergedPRLookup` shipped in #593 with a careful doc comment and two unit
// tests, and for its entire life no production call site set it. Every
// behavioural test passed, because each supplied the callback itself. The
// mechanism was never broken; the wiring never existed. That is the **Unpinned
// Wiring** class in docs/FAILURE_TAXONOMY.md — a guarantee produced by WHERE a
// thing is constructed, which the mechanism's own tests structurally cannot
// see — and the only test that can see it is one that looks at the
// construction sites.
//
// So: every non-test composite literal of WorktreeSweepOptions or
// StrandedBranchOptions in the tree must name MergedPRLookup. Setting it to a
// nil-returning expression is fine and is the documented closed door; OMITTING
// it is the failure, because omission is invisible and silent — exactly how
// this lasted from #593 to #916.
//
// Tests are exempt: a test that constructs its own door is doing the right
// thing, and requiring the field there would be noise.
func TestEveryProductionSweepCallSiteOpensTheMergedPRDoor(t *testing.T) {
	root := repoRoot(t)

	watched := map[string]bool{
		"WorktreeSweepOptions":  true,
		"StrandedBranchOptions": true,
	}

	var missing []string
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "out":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this test cannot parse is not a finding about wiring.
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			name := litTypeName(lit)
			if !watched[name] {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "MergedPRLookup" {
					return true
				}
			}
			rel, _ := filepath.Rel(root, path)
			missing = append(missing, rel+":"+itoaLine(fset, lit.Pos())+" "+name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("production sweep call site(s) omit MergedPRLookup, so the second door is closed there and nothing says so:\n  %s\n"+
			"Set the field — a nil-returning expression is the documented closed door; omission is the #593 bug.",
			strings.Join(missing, "\n  "))
	}

	// A guard against the guard: if the walk found no literals at all, this
	// test is vacuous and would stay green through any refactor that renamed
	// the types out from under it.
	if countWatchedLiterals(t, root, watched) == 0 {
		t.Fatal("found no production WorktreeSweepOptions/StrandedBranchOptions literals at all — this test is asserting nothing")
	}
}

func litTypeName(lit *ast.CompositeLit) string {
	switch t := lit.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func itoaLine(fset *token.FileSet, pos token.Pos) string {
	return strings.TrimPrefix(fset.Position(pos).String(), fset.Position(pos).Filename+":")
}

func countWatchedLiterals(t *testing.T, root string, watched map[string]bool) int {
	t.Helper()
	fset := token.NewFileSet()
	count := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "out":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && watched[litTypeName(lit)] {
				count++
			}
			return true
		})
		return nil
	})
	return count
}

// repoRoot walks up from the package directory to the module root, so the test
// is independent of where `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root (no go.mod above the package directory)")
	return ""
}
