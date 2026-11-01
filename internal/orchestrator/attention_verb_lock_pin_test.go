package orchestrator

// Static pin for #2750: nothing a verb can reach may touch the attention store
// on the verb's own goroutine. Store.Resolve holds the store lock across
// ExecuteVerb and that lock is not reentrant, so an inline call deadlocks, as
// ResumeRepo's did on Retry and NotifyComplete's did on mark-done (#2528).
//
// The roots are derived from ExecuteVerb, so a new verb needs no table entry.
// Calls resolve through receivers, parameters, local aliases, struct fields and
// result types by name. Function-valued fields and interface calls are not
// followed; a walk that over-matches fails safe and is fixed by dispatching.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// offLockCalls run their function arguments on a tracked goroutine.
var offLockCalls = map[string]bool{"dispatchAttention": true, "goTracked": true, "goTrackedBoardOp": true}

// attentionSinks reach the store through a wrapper whose name is the violation.
var attentionSinks = map[string]bool{"raiseThrough": true, "raiseAttention": true, "autoResolveAttention": true}

const attentionStoreType = "attention.Store"

type walkedFunc struct {
	key     string // "Type.Method", or ".func" for a package function
	decl    *ast.FuncDecl
	recv    string
	recvTyp string
	result  string
}

// walkedPackage is the name-level view of one package's source.
type walkedPackage struct {
	fset    *token.FileSet
	funcs   map[string]*walkedFunc
	fields  map[string]map[string]string // type → field → type
	methods map[string][]string          // type → method keys
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name
		}
	}
	return ""
}

func newWalkedPackage(fset *token.FileSet, files []*ast.File) *walkedPackage {
	p := &walkedPackage{
		fset:    fset,
		funcs:   map[string]*walkedFunc{},
		fields:  map[string]map[string]string{},
		methods: map[string][]string{},
	}
	for _, file := range files {
		for _, d := range file.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				f := &walkedFunc{key: "." + decl.Name.Name, decl: decl}
				if decl.Recv != nil && len(decl.Recv.List) == 1 {
					field := decl.Recv.List[0]
					f.recvTyp = typeName(field.Type)
					if len(field.Names) == 1 {
						f.recv = field.Names[0].Name
					}
					f.key = f.recvTyp + "." + decl.Name.Name
					p.methods[f.recvTyp] = append(p.methods[f.recvTyp], f.key)
				}
				if r := decl.Type.Results; r != nil && len(r.List) > 0 {
					f.result = typeName(r.List[0].Type)
				}
				p.funcs[f.key] = f
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					fields := map[string]string{}
					for _, field := range st.Fields.List {
						for _, name := range field.Names {
							fields[name.Name] = typeName(field.Type)
						}
					}
					p.fields[ts.Name.Name] = fields
				}
			}
		}
	}
	return p
}

// funcScope tracks the static types of one function's receiver, parameters and
// local aliases.
type funcScope struct {
	pkg    *walkedPackage
	fn     *walkedFunc
	locals map[string]string
}

func (s *funcScope) exprType(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.exprType(x.X)
	case *ast.Ident:
		return s.locals[x.Name]
	case *ast.SelectorExpr:
		return s.pkg.fields[s.exprType(x.X)][x.Sel.Name]
	case *ast.CallExpr:
		if key := s.callee(x); key != "" {
			if f := s.pkg.funcs[key]; f != nil {
				return f.result
			}
		}
	}
	return ""
}

// callee returns the local function a call resolves to, or "".
func (s *funcScope) callee(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		if _, ok := s.pkg.funcs["."+fn.Name]; ok {
			return "." + fn.Name
		}
	case *ast.SelectorExpr:
		key := s.exprType(fn.X) + "." + fn.Sel.Name
		if _, ok := s.pkg.funcs[key]; ok {
			return key
		}
	}
	return ""
}

func calledName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// reachedCall is one call found on a function reachable from the start set.
type reachedCall struct {
	chain []string
	call  *ast.CallExpr
	scope *funcScope
}

// walk visits every call that runs on the caller's goroutine, reachable from
// start. It does not descend into `go` statements or off-lock arguments.
// visit returning false stops the walk from following that call.
func (p *walkedPackage) walk(start []string, visit func(reachedCall) bool) {
	type item struct {
		key   string
		chain []string
	}
	seen := map[string]bool{}
	var queue []item
	push := func(key string, chain []string) {
		if !seen[key] && p.funcs[key] != nil {
			seen[key] = true
			queue = append(queue, item{key, append(append([]string(nil), chain...), key)})
		}
	}
	for _, key := range start {
		push(key, nil)
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		fn := p.funcs[it.key]
		if fn.decl.Body == nil {
			continue
		}
		scope := &funcScope{pkg: p, fn: fn, locals: map[string]string{}}
		if fn.recv != "" {
			scope.locals[fn.recv] = fn.recvTyp
		}
		for _, field := range fn.decl.Type.Params.List {
			for _, name := range field.Names {
				scope.locals[name.Name] = typeName(field.Type)
			}
		}
		ast.Inspect(fn.decl.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.GoStmt:
				return false
			case *ast.AssignStmt:
				if x.Tok == token.DEFINE && len(x.Lhs) == len(x.Rhs) {
					for i, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							if typ := scope.exprType(x.Rhs[i]); typ != "" {
								scope.locals[id.Name] = typ
							}
						}
					}
				}
			case *ast.CompositeLit:
				// An adapter handed to another package carries verbs out of
				// reach of a call walk, so its methods are reachable too.
				for _, key := range p.methods[typeName(x.Type)] {
					push(key, it.chain)
				}
			case *ast.CallExpr:
				if offLockCalls[calledName(x)] {
					return false
				}
				if visit(reachedCall{chain: it.chain, call: x, scope: scope}) {
					if key := scope.callee(x); key != "" {
						push(key, it.chain)
					}
				}
			}
			return true
		})
	}
}

// inlineAttentionViolations names every chain from roots to inline store I/O.
func inlineAttentionViolations(p *walkedPackage, roots []string) []string {
	found := map[string]bool{}
	p.walk(roots, func(r reachedCall) bool {
		name, sel := calledName(r.call), ""
		if fn, ok := r.call.Fun.(*ast.SelectorExpr); ok && r.scope.exprType(fn.X) == attentionStoreType {
			sel = "store." + name
		}
		if attentionSinks[name] {
			sel = name
		}
		if sel == "" {
			return true
		}
		found[fmt.Sprintf("%s → %s (%s)", strings.Join(r.chain, " → "), sel,
			p.fset.Position(r.call.Pos()))] = true
		return false
	})
	out := make([]string, 0, len(found))
	for v := range found {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// verbRoots walks ExecuteVerb and returns the orchestrator functions it
// reaches, plus any inline store I/O on the ipc side.
func verbRoots(p *walkedPackage) (roots, inline []string) {
	set := map[string]bool{}
	p.walk([]string{"Server.ExecuteVerb"}, func(r reachedCall) bool {
		fn, ok := r.call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "orchestrator" {
			set["."+fn.Sel.Name] = true
			return true
		}
		switch typ := r.scope.exprType(fn.X); {
		case strings.HasPrefix(typ, "orchestrator."):
			set[strings.TrimPrefix(typ, "orchestrator.")+"."+fn.Sel.Name] = true
		case typ == attentionStoreType:
			inline = append(inline, fmt.Sprintf("%s → store.%s (%s)",
				strings.Join(r.chain, " → "), fn.Sel.Name, p.fset.Position(r.call.Pos())))
		}
		return true
	})
	for k := range set {
		roots = append(roots, k)
	}
	sort.Strings(roots)
	return roots, inline
}

func parseSourceDir(t *testing.T, fset *token.FileSet, dir string) *walkedPackage {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no Go source in %s: %v", dir, err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
	}
	return newWalkedPackage(fset, files)
}

// TestNoInlineAttentionReachableFromAVerb is the AC2 pin.
func TestNoInlineAttentionReachableFromAVerb(t *testing.T) {
	fset := token.NewFileSet()
	ipc := parseSourceDir(t, fset, filepath.Join("..", "ipc"))
	roots, inline := verbRoots(ipc)

	have := map[string]bool{}
	for _, r := range roots {
		have[r] = true
	}
	for _, want := range []string{
		"AutonomousScheduler.Resume",
		"AutonomousScheduler.ResumeRepo",
		"AutonomousScheduler.NotifyComplete",
		"Scheduler.QueueAddItem",
	} {
		if !have[want] {
			t.Fatalf("root %s not derived from ExecuteVerb (got %v) — the walk went blind; a renamed "+
				"field or method would otherwise pass this pin empty", want, roots)
		}
	}

	violations := append(inline, inlineAttentionViolations(parseSourceDir(t, fset, "."), roots)...)
	for _, v := range violations {
		t.Errorf("attention I/O inline on a verb path: %s\n  a verb runs under the store lock, which is "+
			"not reentrant; route it through dispatchAttention (#2750)", v)
	}
}

// TestVerbWalkFlagsInlineAndPassesDispatched proves the walker can go red.
func TestVerbWalkFlagsInlineAndPassesDispatched(t *testing.T) {
	const src = `package x
type Store struct{}
type AS struct { attention *attention.Store; inner *Inner }
type Inner struct { attention *attention.Store }
func (as *AS) direct() { as.attention.List() }
func (as *AS) wrapped() { as.helper() }
func (as *AS) helper() { as.raiseAttention(1) }
func (as *AS) viaField() { as.inner.hop() }
func (i *Inner) hop() { store := i.attention; store.Raise() }
func (as *AS) deferred() { defer as.direct() }
func (as *AS) dispatched() { as.dispatchAttention(as.direct); as.dispatchAttention(func() { as.direct() }) }
func (as *AS) tracked() { as.goTracked(func() { as.direct() }) }
func (as *AS) spawned() { go func() { as.direct() }() }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	pkg := newWalkedPackage(fset, []*ast.File{file})

	for root, want := range map[string]bool{
		"AS.direct": true, "AS.wrapped": true, "AS.viaField": true, "AS.deferred": true,
		"AS.dispatched": false, "AS.tracked": false, "AS.spawned": false,
	} {
		if got := len(inlineAttentionViolations(pkg, []string{root})) > 0; got != want {
			t.Errorf("%s flagged = %v, want %v", root, got, want)
		}
	}
}
