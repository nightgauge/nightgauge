package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
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

// --- #907: the cache's cross-repo sharing, pinned at the seam --------------
//
// #845's headline win is that several repos on ONE shared board collapse to a
// single 34-point read per sweep. That guarantee does not live in the
// boardcache package — those tests hand `Wrap` two boards in isolation and
// never observe who HOLDS the cache. It lives in one declaration here: `boards`
// sits in cachedSweepForgeClient's shared `var` block, so every repo's wrapped
// client reads through the same *boardcache.Cache.
//
// Move that declaration inside the returned closure — a plausible-looking
// "scope it tightly" cleanup — and the cache silently becomes per-repo. Every
// shared-board workspace pays a full board read per repo again, and before
// these tests the entire suite stayed green. That is the `decoration` /
// `cannot-go-red` class in docs/FAILURE_TAXONOMY.md: a real mechanism whose
// guarantee nothing enforces.
//
// TestSweepForgeClientCachesOneRouterPerBoard is NOT this test. A router and a
// cache are two different objects with two different lifetimes, and only the
// router's was asserted.

// countingSweepBoard records how many times the board behind the cache was
// actually read. It stands in for the forge adapter's BoardService, so a hit
// here means real GraphQL points were spent.
type countingSweepBoard struct {
	listOpen int
}

func (b *countingSweepBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	b.listOpen++
	return nil, 0, nil
}

func (b *countingSweepBoard) ListItems(context.Context, string) ([]forgetypes.BoardItem, error) {
	return nil, nil
}

func (b *countingSweepBoard) GetItem(context.Context, string, string, int) (*forgetypes.BoardItem, error) {
	return nil, nil
}

// countingSweepClient is a ForgeClient whose only live service is the board.
// Project() must return a typed nil rather than fall through to the embedded
// nil interface: boardcache.WrapClient dereferences it (see
// TestNilProjectStaysNil).
type countingSweepClient struct {
	forge.ForgeClient
	board forge.BoardService
}

func (c *countingSweepClient) Board() forge.BoardService     { return c.board }
func (c *countingSweepClient) Project() forge.ProjectService { return nil }

// countingSweepBoards installs an adapter whose clients share ONE board per
// (owner, project) — the physical reality the cache is meant to protect: two
// repos on the same board are two clients in front of one GitHub project.
// Returns the per-board counters so a test can assert reads, not resolutions.
func countingSweepBoards(t *testing.T) map[int]*countingSweepBoard {
	t.Helper()
	const kind = forge.Kind("counting-board-907")
	boards := map[int]*countingSweepBoard{}

	forge.RegisterAdapter(kind, func(cfg forge.Config) (forge.ForgeClient, error) {
		b := boards[cfg.ProjectNumber]
		if b == nil {
			b = &countingSweepBoard{}
			boards[cfg.ProjectNumber] = b
		}
		return &countingSweepClient{board: b}, nil
	})

	prev := buildSweepRouter
	t.Cleanup(func() { buildSweepRouter = prev })
	buildSweepRouter = func(root, owner string, project int, ownerType string) (*forge.Router, error) {
		r := forge.NewRouter()
		r.Register(string(kind), forge.Config{Kind: kind, Owner: owner, ProjectNumber: project, OwnerType: ownerType})
		r.SetDefault(string(kind))
		return r, nil
	}
	return boards
}

func TestSweepForgeClientSharesOneBoardSnapshotAcrossRepos(t *testing.T) {
	// Two siblings deliberately declare the SAME board as the primary: this is
	// the shared-board workspace #845 was measured against.
	root := writeSweepWorkspace(t, "acme", 3, map[string]int{"platform": 3, "flutter": 3})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	boards := countingSweepBoards(t)

	resolve := cachedSweepForgeClient(root, cfg)
	for _, repo := range []string{"acme/primary", "acme/platform", "acme/flutter"} {
		client, err := resolve(repo)
		if err != nil {
			t.Fatalf("resolve %s: %v", repo, err)
		}
		if _, _, err := client.Board().ListOpenItems(context.Background()); err != nil {
			t.Fatalf("list open items for %s: %v", repo, err)
		}
	}

	got := boards[3].listOpen
	if got != 1 {
		t.Fatalf("board 3 was read %d times across 3 repos that share it, want 1 — "+
			"a per-repo cache costs a full 34-point read per repo and undoes #845 "+
			"(check that `boards` is still declared in cachedSweepForgeClient's "+
			"shared var block, not inside the returned closure)", got)
	}
}

func TestSweepForgeClientDoesNotShareSnapshotsAcrossDistinctBoards(t *testing.T) {
	// The counter-test to the one above: a cache that over-shares would satisfy
	// "one read for three repos" by serving board 4's repo from board 3's
	// snapshot — answering with another board's items, which is worse than
	// paying for the read.
	root := writeSweepWorkspace(t, "acme", 3, map[string]int{"platform": 4})
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load primary config: %v", err)
	}
	boards := countingSweepBoards(t)

	resolve := cachedSweepForgeClient(root, cfg)
	for _, repo := range []string{"acme/primary", "acme/platform"} {
		client, err := resolve(repo)
		if err != nil {
			t.Fatalf("resolve %s: %v", repo, err)
		}
		if _, _, err := client.Board().ListOpenItems(context.Background()); err != nil {
			t.Fatalf("list open items for %s: %v", repo, err)
		}
	}

	for _, project := range []int{3, 4} {
		b := boards[project]
		if b == nil {
			t.Fatalf("board %d was never read — a distinct board must be read on its own, not served from another board's snapshot", project)
		}
		if b.listOpen != 1 {
			t.Errorf("board %d was read %d times, want exactly 1", project, b.listOpen)
		}
	}
}
