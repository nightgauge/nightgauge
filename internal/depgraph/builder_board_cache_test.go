package depgraph

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// countingBoard is a forge.BoardService that answers ListOpenItems from a
// fixed board and counts how many times it was asked.
type countingBoard struct {
	forge.BoardService
	items []forgetypes.BoardItem
	reads atomic.Int32
}

func (b *countingBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	b.reads.Add(1)
	return b.items, len(b.items), nil
}

// TestBuildGraph_SharedBoardIsReadOnce pins the #845 property at the builder
// level: N repos bound to the same (owner, project) is ONE board read, not N.
// Before this fix the builder issued gh.NewBoardService(...).ListOpenItems per
// repo, so a six-repo shared-board workspace paid for six full reads of one
// board at every daemon start.
func TestBuildGraph_SharedBoardIsReadOnce(t *testing.T) {
	board := &countingBoard{items: []forgetypes.BoardItem{
		{Number: 1, Title: "a", State: "OPEN", Status: "Ready", Repo: "O/a"},
		{Number: 2, Title: "b", State: "OPEN", Status: "Ready", Repo: "O/b"},
		{Number: 3, Title: "c", State: "OPEN", Status: "Backlog", Repo: "O/c",
			BlockedBy: []types.BlockingRef{{Repo: "O/a", Number: 1}}},
	}}
	other := &countingBoard{items: []forgetypes.BoardItem{
		{Number: 9, Title: "z", State: "OPEN", Status: "Ready", Repo: "O/z"},
	}}
	provider := func(repo RepoConfig) forge.BoardService {
		if repo.Project == 7 {
			return board
		}
		return other
	}
	repos := []RepoConfig{
		{Owner: "O", Name: "a", Project: 7},
		{Owner: "O", Name: "b", Project: 7},
		{Owner: "O", Name: "c", Project: 7},
		{Owner: "O", Name: "z", Project: 8},
	}
	fetcher := func(ctx context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		return provider(repo).ListOpenItems(ctx)
	}
	bodies := func(context.Context, string, string, int) (string, error) { return "", nil }

	g, err := buildGraphFromFetcher(context.Background(), fetcher, bodies, repos, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := board.reads.Load(); got != 1 {
		t.Errorf("shared board (project 7, 3 repos) read %d times, want 1", got)
	}
	if got := other.reads.Load(); got != 1 {
		t.Errorf("project 8 read %d times, want 1", got)
	}
	if len(g.Nodes) != 4 {
		t.Errorf("graph has %d nodes, want 4 (dedup must not drop items)", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Errorf("graph has %d edges, want 1 blockedBy edge", len(g.Edges))
	}
}

// TestCachedBoardProvider_WrapsSharedCache pins that the provider a daemon
// hands the builder actually reaches the snapshot cache: two builds inside
// the TTL, across repos on one board, are one underlying read.
func TestCachedBoardProvider_WrapsSharedCache(t *testing.T) {
	inner := &countingBoard{items: []forgetypes.BoardItem{
		{Number: 1, Title: "a", State: "OPEN", Status: "Ready", Repo: "O/a"},
	}}
	cache := boardcache.New(0)
	provider := func(repo RepoConfig) forge.BoardService {
		return cache.Wrap(inner, repo.Owner, repo.Project)
	}
	fetcher := func(ctx context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		return provider(repo).ListOpenItems(ctx)
	}
	bodies := func(context.Context, string, string, int) (string, error) { return "", nil }
	repos := []RepoConfig{{Owner: "O", Name: "a", Project: 7}, {Owner: "O", Name: "b", Project: 7}}
	for i := 0; i < 2; i++ {
		if _, err := buildGraphFromFetcher(context.Background(), fetcher, bodies, repos, nil); err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if got := inner.reads.Load(); got != 1 {
		t.Errorf("two builds through the cache issued %d reads, want 1", got)
	}
}
