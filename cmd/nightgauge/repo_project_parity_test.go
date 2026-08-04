package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// #313: "which board does repo X use?" had four independent answers — the bare
// top-level project for every repo (autoDetectRepos), the member config with a
// manifest fallback (reposFromWorkspaceManifest), the member config with a flag
// fallback (detectSiblingRepos), and a refusal (ResolveRepoProjectNumber). On
// the nightgauge workspace that produced three different numbers for the same
// repo, and a doctor diagnostic written against one of them stated a falsehood
// about another (#280).
//
// These tests pin the two properties that keep them from drifting apart again:
// the scheduler and the CLI must AGREE on every declared repo, and there must
// remain exactly one place in this file where a RepoConfig gets its board.

// TestSchedulerAndCLIAgree_SharedBoardWorkspace is the parity assertion. Each
// member declares its own board; the scheduler path and the filing path must
// return the same number for every one of them.
func TestSchedulerAndCLIAgree_SharedBoardWorkspace(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, root, "acme", "web", 3)
	writeRepoConfig(t, filepath.Join(root, "platform"), "acme", "platform", 4)
	writeRepoConfig(t, filepath.Join(root, "mobile"), "acme", "mobile", 5)
	writeWorkspaceManifest(t, root, `repositories:
  - name: web
    path: .
    project_number: 3
  - name: platform
    path: platform
    project_number: 4
  - name: mobile
    path: mobile
    project_number: 5
`)

	cfg := &config.Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}

	// Scheduler path: the repo set the autonomous loop actually polls.
	schedulerBoards := map[string]int{}
	for _, rc := range reposFromWorkspaceManifest(cfg, root, "acme", 3) {
		schedulerBoards[rc.Name] = rc.Project
	}
	if len(schedulerBoards) != 3 {
		t.Fatalf("scheduler saw %d repos, want 3: %+v", len(schedulerBoards), schedulerBoards)
	}

	// CLI path: what `nightgauge project resolve --repo X` answers.
	for name, schedulerBoard := range schedulerBoards {
		cliBoard, err := resolveProjectNumber(cfg, false, 3, "acme", name, root)
		if err != nil {
			t.Fatalf("%s: CLI path refused a repo the scheduler polls: %v", name, err)
		}
		if cliBoard != schedulerBoard {
			t.Errorf("%s: scheduler polls board %d, CLI files to board %d", name, schedulerBoard, cliBoard)
		}
	}

	// The boards must be the DECLARED ones, not one board repeated — a
	// scheduler that assigned the top-level project to everything would agree
	// with a CLI that did the same, and both would be wrong.
	for name, want := range map[string]int{"web": 3, "platform": 4, "mobile": 5} {
		if got := schedulerBoards[name]; got != want {
			t.Errorf("%s: board = %d, want %d (the board that repo declares)", name, got, want)
		}
	}
}

// TestSchedulerPollsSharedBoardWhereCLIRefuses pins the one place the two
// policies legitimately differ, so the difference stays deliberate and named
// rather than being rediscovered as a bug. A repo nothing declares a board for
// is polled on the workspace default (wasting a scan at worst) but never filed
// against (which would misroute an issue — #3232).
func TestSchedulerPollsSharedBoardWhereCLIRefuses(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, root, "acme", "web", 3)
	cfg := &config.Config{Owner: "acme", DefaultRepo: "web", ProjectNumber: 3}

	res := config.ResolveRepoProject(cfg, config.RepoProjectQuery{
		Owner: "acme", Repo: "undeclared", SharedBoard: 3,
	})
	if res.Source != config.RepoProjectSharedBoardDefault || res.Number != 3 {
		t.Fatalf("scheduler answer = {%d %s}, want {3 %s}",
			res.Number, res.Source, config.RepoProjectSharedBoardDefault)
	}

	if _, err := resolveProjectNumber(cfg, false, 3, "acme", "undeclared", root); err == nil {
		t.Error("the filing path must refuse a board nothing declared, even though the scheduler polls it")
	}
}

// TestRepoConfigBoardsComeFromOneResolver is the drift guard — the
// config-resolution equivalent of the terminal-parity manifest. Every
// depgraph.RepoConfig in this file must get its Project from
// schedulerRepoConfig, because a hand-built literal is exactly how the fourth
// divergent answer appeared and it fails silently: the scheduler polls a real
// board, finds real issues, and nothing anywhere reports a problem.
//
// If this fails, do not add an exemption. Route the new construction site
// through schedulerRepoConfig, or answer why that repo's board is a different
// question from the one every other caller asks.
func TestRepoConfigBoardsComeFromOneResolver(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	// The single sanctioned construction site.
	const sanctioned = "schedulerRepoConfig"

	var offenders []string
	var enclosing string
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			enclosing = fn.Name.Name
			return true
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RepoConfig" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "depgraph" {
			return true
		}
		if enclosing == sanctioned {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Project" {
				continue
			}
			offenders = append(offenders, enclosing+" (main.go:"+
				fset.Position(kv.Pos()).String()[len(fset.Position(kv.Pos()).Filename)+1:]+")")
		}
		return true
	})

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("depgraph.RepoConfig.Project set outside %s(): %v\n"+
			"Every repo→board answer must come from config.ResolveRepoProject (#313). "+
			"Route the construction site through %s instead of assigning Project directly.",
			sanctioned, offenders, sanctioned)
	}
}
