package depgraph

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// -- boardItemToNode tests --

func TestBoardItemToNode_Basic(t *testing.T) {
	item := &types.BoardItem{
		Number:   42,
		Title:    "Implement feature",
		State:    "OPEN",
		Status:   "Ready",
		Size:     "M",
		Priority: "P1",
		Labels:   []string{"feature", "backend"},
		Repo:     "",
	}
	node := boardItemToNode(item, "nightgauge/nightgauge")

	if node.Number != 42 {
		t.Errorf("expected Number 42, got %d", node.Number)
	}
	if node.Title != "Implement feature" {
		t.Errorf("expected Title 'Implement feature', got %q", node.Title)
	}
	if node.State != "OPEN" {
		t.Errorf("expected State OPEN, got %q", node.State)
	}
	if node.BoardStatus != "Ready" {
		t.Errorf("expected BoardStatus 'Ready', got %q", node.BoardStatus)
	}
	if node.Repo != "nightgauge/nightgauge" {
		t.Errorf("expected Repo 'nightgauge/nightgauge', got %q", node.Repo)
	}
	if node.Size != "M" {
		t.Errorf("expected Size 'M', got %q", node.Size)
	}
	if node.Weight != 3 {
		t.Errorf("expected Weight 3 (M), got %d", node.Weight)
	}
	if node.EpicNumber != 0 {
		t.Errorf("expected EpicNumber 0 (no parent), got %d", node.EpicNumber)
	}
}

func TestBoardItemToNode_WithEpic(t *testing.T) {
	item := &types.BoardItem{
		Number:       100,
		Title:        "Sub-issue",
		State:        "OPEN",
		Repo:         "nightgauge/nightgauge",
		Size:         "S",
		ParentNumber: 50,
	}
	node := boardItemToNode(item, "nightgauge/nightgauge")
	if node.EpicNumber != 50 {
		t.Errorf("expected EpicNumber 50, got %d", node.EpicNumber)
	}
}

func TestBoardItemToNode_SizeWeightMapping(t *testing.T) {
	tests := []struct {
		size           types.Size
		expectedWeight int
	}{
		{types.SizeXS, 1},
		{types.SizeS, 2},
		{types.SizeM, 3},
		{types.SizeL, 5},
		{types.SizeXL, 8},
		{"", 3},     // unknown/empty → default medium weight
		{"HUGE", 3}, // unrecognised → default
	}
	for _, tt := range tests {
		item := &types.BoardItem{
			Number: 1,
			Title:  "Test",
			State:  "OPEN",
			Size:   tt.size,
		}
		node := boardItemToNode(item, "R/repo")
		if node.Weight != tt.expectedWeight {
			t.Errorf("size %q: expected weight %d, got %d", tt.size, tt.expectedWeight, node.Weight)
		}
	}
}

func TestBoardItemToNode_EffectiveRepoResolution(t *testing.T) {
	t.Run("uses item.Repo when set", func(t *testing.T) {
		item := &types.BoardItem{
			Number: 1,
			Title:  "Issue",
			State:  "OPEN",
			Repo:   "nightgauge/platform", // item has its own repo
		}
		// repoName is the board's repo, but item.Repo overrides it
		node := boardItemToNode(item, "nightgauge/nightgauge")
		if node.Repo != "nightgauge/platform" {
			t.Errorf("expected item.Repo 'nightgauge/platform', got %q", node.Repo)
		}
	})

	t.Run("falls back to repoName when item.Repo is empty", func(t *testing.T) {
		item := &types.BoardItem{
			Number: 2,
			Title:  "Issue",
			State:  "OPEN",
			Repo:   "",
		}
		node := boardItemToNode(item, "nightgauge/nightgauge")
		if node.Repo != "nightgauge/nightgauge" {
			t.Errorf("expected fallback to repoName 'nightgauge/nightgauge', got %q", node.Repo)
		}
	})
}

// -- edgeKey tests --

func TestEdgeKey_CrossRepo(t *testing.T) {
	e := Edge{
		From: NodeID{Repo: "nightgauge/nightgauge", Number: 100},
		To:   NodeID{Repo: "nightgauge/platform", Number: 200},
		Type: "crossRepo",
	}
	key := edgeKey(e)
	expected := "nightgauge/nightgauge#100->nightgauge/platform#200"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestEdgeKey_SameRepo(t *testing.T) {
	e := Edge{
		From: NodeID{Repo: "nightgauge/nightgauge", Number: 10},
		To:   NodeID{Repo: "nightgauge/nightgauge", Number: 20},
		Type: "blockedBy",
	}
	key := edgeKey(e)
	expected := "nightgauge/nightgauge#10->nightgauge/nightgauge#20"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestEdgeKey_Deterministic(t *testing.T) {
	e := Edge{
		From: NodeID{Repo: "R", Number: 1},
		To:   NodeID{Repo: "R", Number: 2},
	}
	// Same edge struct must always produce the same key.
	for i := 0; i < 10; i++ {
		key := edgeKey(e)
		if key != "R#1->R#2" {
			t.Errorf("edgeKey not deterministic on call %d: got %q", i, key)
		}
	}
}

// -- BuildGraphFromItems edge case tests --

func TestBuildGraphFromItems_EdgeDeduplication(t *testing.T) {
	// Issue A is blockedBy B (graphql) AND A's body also references B (crossRepo).
	// Two sources pointing to the same edge — dedup must keep only 1.
	items := []types.BoardItem{
		{
			Number: 1,
			Title:  "A",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "M",
			BlockedBy: []types.BlockingRef{
				{Number: 2, Repo: "O/core", State: "OPEN"},
			},
		},
		{
			Number: 2,
			Title:  "B",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "S",
		},
	}

	// Body also references #2 — same edge as the blockedBy relationship.
	bodies := map[string]string{
		"O/core#1": "Blocked by core #2",
	}

	workspace := map[string]bool{"O/core": true}
	aliases := map[string]string{"core": "O/core"}

	g := BuildGraphFromItems(items, bodies, workspace, aliases)

	if len(g.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Errorf("expected 1 edge (deduped), got %d: %v", len(g.Edges), g.Edges)
	}
}

func TestBuildGraphFromItems_UnresolvableRefs(t *testing.T) {
	// Body references a repo not in the workspace — edge should be marked unresolvable.
	items := []types.BoardItem{
		{
			Number: 5,
			Title:  "Issue",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "M",
		},
	}

	bodies := map[string]string{
		"O/core#5": "Blocked by nightgauge/external#99",
	}

	workspace := map[string]bool{"O/core": true}
	// "nightgauge/external" is intentionally absent from workspace.

	g := BuildGraphFromItems(items, bodies, workspace, nil)

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	if g.Edges[0].Resolvable {
		t.Error("edge to non-workspace repo should not be resolvable")
	}
	if g.Edges[0].To.Repo != "nightgauge/external" {
		t.Errorf("edge To.Repo: expected 'nightgauge/external', got %q", g.Edges[0].To.Repo)
	}
}

func TestBuildGraphFromItems_MixedEdgeSources(t *testing.T) {
	// A is blocked by B (graphql blockedBy), and A's body references C (crossRepo).
	// Both edges should be present with correct edge types.
	items := []types.BoardItem{
		{
			Number: 1,
			Title:  "A",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "M",
			BlockedBy: []types.BlockingRef{
				{Number: 2, Repo: "O/core", State: "OPEN"},
			},
		},
		{
			Number: 2,
			Title:  "B",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "S",
		},
		{
			Number: 10,
			Title:  "C",
			State:  "OPEN",
			Repo:   "O/platform",
			Size:   "L",
		},
	}

	bodies := map[string]string{
		"O/core#1": "Also depends on platform #10",
	}

	workspace := map[string]bool{
		"O/core":     true,
		"O/platform": true,
	}
	aliases := map[string]string{"platform": "O/platform"}

	g := BuildGraphFromItems(items, bodies, workspace, aliases)

	if len(g.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 2 {
		t.Errorf("expected 2 edges (blockedBy + crossRepo), got %d", len(g.Edges))
	}

	edgeTypes := make(map[string]bool)
	for _, e := range g.Edges {
		edgeTypes[e.Type] = true
	}
	if !edgeTypes["blockedBy"] {
		t.Error("expected a 'blockedBy' edge")
	}
	if !edgeTypes["crossRepo"] {
		t.Error("expected a 'crossRepo' edge")
	}
}

func TestBuildGraphFromItems_LargeGraph(t *testing.T) {
	// Linear chain: 1 ← 2 ← 3 ← … ← 20 (each issue blocked by the next)
	// Expected: 20 waves (each node in its own wave).
	const n = 20
	items := make([]types.BoardItem, n)
	for i := 0; i < n; i++ {
		item := types.BoardItem{
			Number: i + 1,
			Title:  fmt.Sprintf("Issue %d", i+1),
			State:  "OPEN",
			Repo:   "O/repo",
			Size:   "S",
		}
		if i > 0 {
			// Issue (i+1) is blocked by issue i — forms the chain
			item.BlockedBy = []types.BlockingRef{
				{Number: i, Repo: "O/repo", State: "OPEN"},
			}
		}
		items[i] = item
	}

	workspace := map[string]bool{"O/repo": true}

	g := BuildGraphFromItems(items, nil, workspace, nil)

	if len(g.Nodes) != n {
		t.Errorf("expected %d nodes, got %d", n, len(g.Nodes))
	}
	if len(g.Edges) != n-1 {
		t.Errorf("expected %d edges, got %d", n-1, len(g.Edges))
	}
	// A linear chain of n nodes has no parallelism: each node can only start
	// after the previous completes, so each wave contains exactly one node.
	if len(g.Waves) != n {
		t.Errorf("expected %d waves for linear chain of %d nodes, got %d — each node should be in its own sequential wave", n, n, len(g.Waves))
	}
}

// -- BuildGraph error path tests --

func TestBuildGraph_EmptyRepos(t *testing.T) {
	_, err := BuildGraph(context.Background(), nil, []RepoConfig{}, nil)
	if err == nil {
		t.Error("expected error for empty repos, got nil")
	}
}

// -- buildGraphFromFetcherWithBatch tests (Issue #3400) --

// TestBuildGraphFromFetcherWithBatch_OneCallPerRepo verifies that the bulk
// path issues exactly one body-batch call per repo regardless of how many
// nodes that repo contributed. Pre-#3400 the autonomous startup made one
// GetIssue call per node — ~80 sequential GitHub round-trips — which delayed
// the first dispatch by ~2 minutes.
func TestBuildGraphFromFetcherWithBatch_OneCallPerRepo(t *testing.T) {
	fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		switch repo.FullName() {
		case "O/a":
			return []types.BoardItem{
				{Number: 1, Title: "a1", State: "OPEN", Repo: "O/a", Size: "S"},
				{Number: 2, Title: "a2", State: "OPEN", Repo: "O/a", Size: "S"},
				{Number: 3, Title: "a3", State: "OPEN", Repo: "O/a", Size: "S"},
			}, 3, nil
		case "O/b":
			return []types.BoardItem{
				{Number: 10, Title: "b10", State: "OPEN", Repo: "O/b", Size: "S"},
				{Number: 11, Title: "b11", State: "OPEN", Repo: "O/b", Size: "S"},
			}, 2, nil
		}
		return nil, 0, nil
	}

	// Per-issue fetcher MUST NOT be called in the bulk path. If we observe a
	// call here, the bulk path silently fell through and the perf fix is
	// regressed.
	perIssueCalls := 0
	perIssueFetcher := func(_ context.Context, _, _ string, _ int) (string, error) {
		perIssueCalls++
		return "", nil
	}

	// Bulk fetcher records one call per repo. Returns deterministic bodies.
	type batchCall struct {
		owner, name string
		count       int
	}
	var batchCalls []batchCall
	bodiesBatch := func(_ context.Context, owner, name string, numbers []int) (map[int]string, error) {
		batchCalls = append(batchCalls, batchCall{owner: owner, name: name, count: len(numbers)})
		out := make(map[int]string, len(numbers))
		for _, n := range numbers {
			out[n] = "" // body irrelevant for this test
		}
		return out, nil
	}

	repos := []RepoConfig{
		{Owner: "O", Name: "a", Project: 1},
		{Owner: "O", Name: "b", Project: 2},
	}
	g, err := buildGraphFromFetcherWithBatch(context.Background(), fetcher, perIssueFetcher, bodiesBatch, repos, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(g.Nodes) != 5 {
		t.Errorf("nodes = %d, want 5", len(g.Nodes))
	}
	if perIssueCalls != 0 {
		t.Errorf("per-issue fetcher must not be invoked when bulk fetcher succeeds; got %d calls", perIssueCalls)
	}
	if len(batchCalls) != 2 {
		t.Fatalf("batch calls = %d, want 2 (one per repo)", len(batchCalls))
	}
	// Check that each call carried the right number of issue numbers per repo.
	got := map[string]int{}
	for _, c := range batchCalls {
		got[c.owner+"/"+c.name] = c.count
	}
	if got["O/a"] != 3 {
		t.Errorf("O/a batch size = %d, want 3", got["O/a"])
	}
	if got["O/b"] != 2 {
		t.Errorf("O/b batch size = %d, want 2", got["O/b"])
	}
}

// TestBuildGraphFromFetcherWithBatch_FallbackOnBatchError verifies that a
// failing bulk fetch for one repo falls back to per-issue calls FOR THAT
// REPO ONLY. Other repos still use the bulk path. This keeps the perf win
// even when one repo's GraphQL endpoint is misbehaving.
func TestBuildGraphFromFetcherWithBatch_FallbackOnBatchError(t *testing.T) {
	fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		switch repo.FullName() {
		case "O/a":
			return []types.BoardItem{
				{Number: 1, Title: "a1", State: "OPEN", Repo: "O/a", Size: "S"},
				{Number: 2, Title: "a2", State: "OPEN", Repo: "O/a", Size: "S"},
			}, 2, nil
		case "O/b":
			return []types.BoardItem{
				{Number: 10, Title: "b10", State: "OPEN", Repo: "O/b", Size: "S"},
			}, 1, nil
		}
		return nil, 0, nil
	}

	perIssueCallsByRepo := map[string]int{}
	perIssueFetcher := func(_ context.Context, owner, name string, _ int) (string, error) {
		perIssueCallsByRepo[owner+"/"+name]++
		return "", nil
	}

	bodiesBatch := func(_ context.Context, owner, name string, numbers []int) (map[int]string, error) {
		if name == "a" {
			return nil, fmt.Errorf("simulated transient GraphQL failure")
		}
		out := make(map[int]string, len(numbers))
		for _, n := range numbers {
			out[n] = ""
		}
		return out, nil
	}

	repos := []RepoConfig{
		{Owner: "O", Name: "a", Project: 1},
		{Owner: "O", Name: "b", Project: 2},
	}
	g, err := buildGraphFromFetcherWithBatch(context.Background(), fetcher, perIssueFetcher, bodiesBatch, repos, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3", len(g.Nodes))
	}
	// O/a's 2 nodes must have hit the per-issue fallback.
	if perIssueCallsByRepo["O/a"] != 2 {
		t.Errorf("O/a per-issue calls after batch fail = %d, want 2", perIssueCallsByRepo["O/a"])
	}
	// O/b's nodes must NOT have triggered per-issue calls — its bulk fetch succeeded.
	if perIssueCallsByRepo["O/b"] != 0 {
		t.Errorf("O/b must not fall back when its bulk fetch succeeded; got %d per-issue calls", perIssueCallsByRepo["O/b"])
	}
}

// -- buildGraphFromFetcher tests --

func TestBuildGraphFromFetcher_FetchError(t *testing.T) {
	failFetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		return nil, 0, fmt.Errorf("simulated pagination failure")
	}
	noopBodyFetcher := func(_ context.Context, owner, name string, number int) (string, error) {
		return "", nil
	}
	repos := []RepoConfig{{Owner: "O", Name: "repo", Project: 1}}

	_, err := buildGraphFromFetcher(context.Background(), failFetcher, noopBodyFetcher, repos, nil)
	if err == nil {
		t.Error("expected error when fetcher fails, got nil")
	}
	if !strings.Contains(err.Error(), "O/repo") {
		t.Errorf("error should mention the repo name; got: %v", err)
	}
}

func TestBuildGraphFromFetcher_HomeBoardStatusWins(t *testing.T) {
	// Same issue (O/dashboard#360) appears on two project boards:
	//   - Its home board (O/dashboard, project=4) marks it Ready
	//   - The platform board (O/platform, project=2) marks it Backlog
	//     because it's cross-listed there for visibility
	// The home-board status must win regardless of processing order — otherwise
	// the autonomous scheduler's status gate rejects an issue that the home
	// board has explicitly marked Ready.
	cases := []struct {
		name     string
		repos    []RepoConfig
		expected string
	}{
		{
			name: "home processed before foreign",
			repos: []RepoConfig{
				{Owner: "O", Name: "dashboard", Project: 4}, // home — Ready
				{Owner: "O", Name: "platform", Project: 2},  // foreign — Backlog
			},
			expected: "Ready",
		},
		{
			name: "home processed after foreign",
			repos: []RepoConfig{
				{Owner: "O", Name: "platform", Project: 2},  // foreign — Backlog
				{Owner: "O", Name: "dashboard", Project: 4}, // home — Ready
			},
			expected: "Ready",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
				switch repo.FullName() {
				case "O/dashboard":
					return []types.BoardItem{
						{Number: 360, Title: "sub-issue", State: "OPEN", Repo: "O/dashboard", Status: "Ready", Size: "S"},
					}, 1, nil
				case "O/platform":
					// Cross-listed dashboard issue with conflicting Backlog status,
					// plus a real platform-home issue.
					return []types.BoardItem{
						{Number: 360, Title: "sub-issue", State: "OPEN", Repo: "O/dashboard", Status: "Backlog", Size: "S"},
						{Number: 1, Title: "platform issue", State: "OPEN", Repo: "O/platform", Status: "Ready", Size: "S"},
					}, 2, nil
				}
				return nil, 0, nil
			}
			noopBodyFetcher := func(_ context.Context, owner, name string, number int) (string, error) {
				return "", nil
			}

			g, err := buildGraphFromFetcher(context.Background(), fetcher, noopBodyFetcher, tc.repos, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			node, ok := g.Nodes["O/dashboard#360"]
			if !ok {
				t.Fatalf("expected node O/dashboard#360 in graph; nodes: %v", keysOf(g.Nodes))
			}
			if node.BoardStatus != tc.expected {
				t.Errorf("BoardStatus: expected %q (home board), got %q", tc.expected, node.BoardStatus)
			}

			// Sanity: platform's own home item should be unaffected.
			plat, ok := g.Nodes["O/platform#1"]
			if !ok {
				t.Fatalf("expected node O/platform#1 in graph")
			}
			if plat.BoardStatus != "Ready" {
				t.Errorf("platform home item BoardStatus: expected Ready, got %q", plat.BoardStatus)
			}
		})
	}
}

func keysOf(m map[string]*Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuildGraphFromFetcher_DroppedItemsCount(t *testing.T) {
	// Fetcher returns 3 items but rawCount=5 (2 were DraftIssues dropped by nodeToItem).
	fetcher := func(_ context.Context, repo RepoConfig) ([]types.BoardItem, int, error) {
		items := []types.BoardItem{
			{Number: 1, Title: "A", State: "OPEN", Repo: "O/repo", Size: "M"},
			{Number: 2, Title: "B", State: "OPEN", Repo: "O/repo", Size: "S"},
			{Number: 3, Title: "C", State: "OPEN", Repo: "O/repo", Size: "S"},
		}
		return items, 5, nil // rawCount=5, only 3 items returned (2 dropped as DraftIssues)
	}
	noopBodyFetcher := func(_ context.Context, owner, name string, number int) (string, error) {
		return "", nil
	}
	repos := []RepoConfig{{Owner: "O", Name: "repo", Project: 1}}

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	g, err := buildGraphFromFetcher(context.Background(), fetcher, noopBodyFetcher, repos, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stats.DroppedItemsCount != 2 {
		t.Errorf("expected DroppedItemsCount=2, got %d", g.Stats.DroppedItemsCount)
	}
	if !strings.Contains(logBuf.String(), "WARN depgraph") {
		t.Errorf("expected WARN log line, got: %q", logBuf.String())
	}
}
