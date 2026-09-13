package depgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// staticBoard is a forge.BoardService serving fixed open items.
type staticBoard struct{ items []types.BoardItem }

func (b staticBoard) ListItems(context.Context, string) ([]types.BoardItem, error) {
	return b.items, nil
}

func (b staticBoard) ListOpenItems(context.Context) ([]types.BoardItem, int, error) {
	return b.items, len(b.items), nil
}

func (b staticBoard) GetItem(context.Context, string, string, int) (*types.BoardItem, error) {
	return nil, forge.ErrNotFound
}

// TestBuildGraphWithBoards_BodyFetchSkipsRelationships: the graph reads issue
// bodies for prose dependencies, and its blockedBy edges come from the board.
// Fetching bodies through the read that completes every relationship
// connection meant an issue whose connections could not be read whole failed
// the body batch and then the per-issue fallback, and its body and prose edges
// were dropped. The fake forge fails any query that selects a relationship
// connection; the body fetch must select none and keep the edge.
func TestBuildGraphWithBoards_BodyFetchSkipsRelationships(t *testing.T) {
	var (
		mu      sync.Mutex
		queries []string
	)
	bodies := map[int]string{10: "## Goal\n\nDepends on: #20\n", 20: ""}
	reRelation := regexp.MustCompile(`\b(subIssues|blockedBy|blocking)\(`)
	reAlias := regexp.MustCompile(`i(\d+): issue\(number: (\d+)\)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		queries = append(queries, req.Query)
		mu.Unlock()
		if reRelation.MatchString(req.Query) {
			http.Error(w, "relationship pages unavailable", http.StatusBadGateway)
			return
		}
		repo := map[string]interface{}{}
		for _, m := range reAlias.FindAllStringSubmatch(req.Query, -1) {
			n, _ := strconv.Atoi(m[2])
			repo["i"+m[1]] = map[string]interface{}{
				"id": fmt.Sprintf("I_%d", n), "number": n, "title": fmt.Sprintf("Issue %d", n),
				"body": bodies[n], "state": "OPEN", "url": "",
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"repository": repo}})
	}))
	t.Cleanup(srv.Close)

	board := staticBoard{items: []types.BoardItem{
		{Number: 10, Title: "Issue 10", State: "OPEN", Repo: "O/a"},
		{Number: 20, Title: "Issue 20", State: "OPEN", Repo: "O/a"},
	}}
	g, err := BuildGraphWithBoards(context.Background(), gh.NewClientWithURL("test-token", srv.URL),
		func(RepoConfig) forge.BoardService { return board },
		[]RepoConfig{{Owner: "O", Name: "a", Project: 1}}, nil)
	if err != nil {
		t.Fatalf("BuildGraphWithBoards: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, q := range queries {
		if m := reRelation.FindString(q); m != "" {
			t.Errorf("body fetch selected a relationship connection (%s):\n%s", m, q)
		}
	}
	found := false
	for _, e := range g.Edges {
		if e.From.String() == "O/a#10" && e.To.String() == "O/a#20" {
			found = true
		}
	}
	if !found {
		t.Fatalf("edges = %+v, want the body-declared O/a#10 -> O/a#20", g.Edges)
	}
}

// TestBuildGraphWithBoards_BodyFetchHonorsRateLimitFloor: when the bulk body
// fetch fails, the graph falls back to one body read per node. Below the
// shared tracker's rate-limit floor both the bulk read and every fallback read
// must stop at the gate, so a failing forge is not sent one request per node
// from the reserved budget. The fake forge fails every request with a 502.
func TestBuildGraphWithBoards_BodyFetchHonorsRateLimitFloor(t *testing.T) {
	t.Setenv("NIGHTGAUGE_GITHUB_RATELIMIT_FLOOR", "100")
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	tr := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	if err := tr.Set("alice", &gh.RateLimitInfo{
		Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	client := gh.NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")

	board := staticBoard{items: []types.BoardItem{
		{Number: 10, Title: "Issue 10", State: "OPEN", Repo: "O/a"},
		{Number: 20, Title: "Issue 20", State: "OPEN", Repo: "O/a"},
		{Number: 30, Title: "Issue 30", State: "OPEN", Repo: "O/a"},
	}}
	g, err := BuildGraphWithBoards(context.Background(), client,
		func(RepoConfig) forge.BoardService { return board },
		[]RepoConfig{{Owner: "O", Name: "a", Project: 1}}, nil)
	if err != nil {
		t.Fatalf("BuildGraphWithBoards: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("nodes = %d, want the 3 board items", len(g.Nodes))
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("body fetch sent %d requests below the rate-limit floor, want 0", requests)
	}
}
