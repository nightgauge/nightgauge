package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// The sweep's forge client used to be built as BuildRouter("", 0, "") — one
// router for the whole workspace, anchored to os.Getwd(). These tests pin the
// two independent defects that produced (#844):
//
//  1. the daemon's cwd is not a checkout, so the project number resolved to 0
//     and every board read failed while still being billed; and
//  2. a single router binds ONE project, so even with a config it would answer
//     every repo with the primary repo's board.

// writeSweepWorkspace builds a workspace whose manifest lists the primary repo
// and one sibling per entry in members, each with its own config.yaml declaring
// the given board. A member with board 0 gets a config with no project at all —
// the "nothing declares a board for this repo" case.
func writeSweepWorkspace(t *testing.T, owner string, sharedBoard int, members map[string]int) string {
	t.Helper()
	ws := t.TempDir()
	primary := filepath.Join(ws, "primary")

	manifest := "repositories:\n  - name: primary\n    path: .\n"
	for name := range members {
		manifest += "  - name: " + name + "\n    path: ../" + name + "\n"
	}
	mustWrite(t, filepath.Join(primary, ".vscode", "nightgauge-workspace.yaml"), manifest)

	primaryCfg := "owner: " + owner + "\nowner_type: org\nrepo: primary\n"
	if sharedBoard > 0 {
		primaryCfg += "project:\n  number: " + itoa(sharedBoard) + "\n"
	}
	mustWrite(t, filepath.Join(primary, ".nightgauge", "config.yaml"), primaryCfg)

	for name, board := range members {
		cfg := "owner: " + owner + "\nowner_type: org\nrepo: " + name + "\n"
		if board > 0 {
			cfg += "project:\n  number: " + itoa(board) + "\n"
		}
		mustWrite(t, filepath.Join(ws, name, ".nightgauge", "config.yaml"), cfg)
	}
	return primary
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// recordSweepRouters replaces the router builder with a recorder, so a test can
// assert not just what a resolution returned but whether a router — the object
// that holds the token and the transport — was constructed at all.
func recordSweepRouters(t *testing.T) *[][3]interface{} {
	t.Helper()
	prev := buildSweepRouter
	t.Cleanup(func() { buildSweepRouter = prev })
	var calls [][3]interface{}
	buildSweepRouter = func(root, owner string, project int, ownerType string) (*forge.Router, error) {
		calls = append(calls, [3]interface{}{root, owner, project})
		r := forge.NewRouter()
		r.Register("github", forge.Config{Kind: forge.KindGitHub, Owner: owner, ProjectNumber: project, OwnerType: ownerType})
		r.SetDefault("github")
		return r, nil
	}
	return &calls
}

func TestSweepForgeClientResolvesEachRepoOwnBoard(t *testing.T) {
	root := writeSweepWorkspace(t, "acme", 3, map[string]int{"platform": 4, "flutter": 5})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	calls := recordSweepRouters(t)

	resolve := cachedSweepForgeClient(root, cfg)
	for _, repo := range []string{"acme/primary", "acme/platform", "acme/flutter"} {
		if _, err := resolve(repo); err != nil {
			t.Fatalf("resolve %s: %v", repo, err)
		}
	}

	var got []int
	for _, c := range *calls {
		got = append(got, c[2].(int))
	}
	want := []int{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("expected one router per board %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("router %d bound project %d, want %d (a workspace-wide router answers every repo with the primary's board — the #844 misroute)", i, got[i], want[i])
		}
	}
	if c := (*calls)[0]; c[0].(string) != root {
		t.Errorf("router anchored at %q, want the workspace root %q — resolving from os.Getwd() is what bound project 0", c[0], root)
	}
}

func TestSweepForgeClientCachesOneRouterPerBoard(t *testing.T) {
	root := writeSweepWorkspace(t, "acme", 3, map[string]int{"platform": 4})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	calls := recordSweepRouters(t)

	resolve := cachedSweepForgeClient(root, cfg)
	for i := 0; i < 3; i++ {
		for _, repo := range []string{"acme/primary", "acme/platform"} {
			if _, err := resolve(repo); err != nil {
				t.Fatalf("resolve %s: %v", repo, err)
			}
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("built %d routers across 6 resolutions of 2 boards, want 2 — rebuilding re-runs the token chain on every sweep", len(*calls))
	}
}

// The acceptance criterion #844 states most sharply: an unresolvable board must
// cost ZERO requests. Before the fix the daemon issued one failing GraphQL query
// per producer per repo per sweep, each billed the same points as a success.
func TestSweepForgeClientSkipsUnmappedRepoWithoutBuildingARouter(t *testing.T) {
	root := writeSweepWorkspace(t, "acme", 0, map[string]int{"orphan": 0})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	calls := recordSweepRouters(t)

	client, err := cachedSweepForgeClient(root, cfg)("acme/orphan")
	if err == nil {
		t.Fatalf("expected a skip for a repo no config maps to a board, got client %#v", client)
	}
	if client != nil {
		t.Errorf("a skipped repo must yield no client, got %#v", client)
	}
	if !strings.Contains(err.Error(), "acme/orphan") {
		t.Errorf("the skip must name the repo it is about, got %q", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("built %d routers for an unresolvable board, want 0 — a router carries the token and the transport, so building one is the first step of spending points", len(*calls))
	}
}

// A shared-board workspace is not the unmapped case: a repo that declares
// nothing legitimately sweeps against the workspace default. Refusing it would
// make the sweep blind to exactly the repos it was extended to cover (#260).
func TestSweepForgeClientUsesSharedBoardForUndeclaredRepo(t *testing.T) {
	root := writeSweepWorkspace(t, "acme", 3, map[string]int{"undeclared": 0})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	calls := recordSweepRouters(t)

	if _, err := cachedSweepForgeClient(root, cfg)("acme/undeclared"); err != nil {
		t.Fatalf("a repo covered by the workspace default board must still be swept: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0][2].(int) != 3 {
		t.Fatalf("expected one router on the shared board 3, got %v", *calls)
	}
}
