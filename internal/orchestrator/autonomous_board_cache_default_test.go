package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// TestAutonomousSchedulerReadsBoardsThroughACacheByConstruction pins that a
// scheduler nobody hands a cache — `nightgauge run --auto` from the CLI, which
// never calls SetBoardCache — still reads its graph-build boards through one
// (#1914). Before, the CLI path read every board uncached, once per repo per
// cycle. The daemon's SetBoardCache still replaces the private cache, and the
// embedded Scheduler follows it so its board writes invalidate the right one.
func TestAutonomousSchedulerReadsBoardsThroughACacheByConstruction(t *testing.T) {
	var boardReads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(req.Query, "projectV2(") || !strings.Contains(req.Query, "items(") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"unexpected query"}]}`))
			return
		}
		boardReads.Add(1)
		_, _ = w.Write([]byte(`{"data":{"organization":{"projectV2":{"id":"PVT_1","title":"Board",` +
			`"items":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`))
	}))
	defer srv.Close()

	client := gh.NewClientWithURL("test-token", srv.URL)
	sched := NewScheduler(client, SchedulerConfig{WorkspaceRoot: t.TempDir(), ProjectNumber: 7})
	as := NewAutonomousScheduler(sched, client, nil, nil, DefaultAutonomousConfig(), layouttest.Repo(t))

	if as.boardCache == nil {
		t.Fatal("NewAutonomousScheduler left boardCache nil: graph builds read boards uncached")
	}
	if sched.boardCache != as.boardCache {
		t.Error("the embedded Scheduler does not hold the scheduler's board cache, so its writes cannot invalidate it")
	}

	ctx := context.Background()
	repo := depgraph.RepoConfig{Owner: "o", Name: "r", Project: 7}
	for i := 0; i < 2; i++ {
		if _, _, err := as.boardProvider(repo).ListOpenItems(ctx); err != nil {
			t.Fatalf("graph-build board read %d: %v", i+1, err)
		}
	}
	if n := boardReads.Load(); n != 1 {
		t.Errorf("board reads = %d for two graph builds inside the TTL, want 1", n)
	}

	shared := boardcache.New(0)
	as.SetBoardCache(shared)
	if as.boardCache != shared || sched.boardCache != shared {
		t.Error("SetBoardCache did not replace the private cache on both schedulers")
	}
	// The shared cache is cold, so the first build reads the board and the
	// second is served from the snapshot the first took there.
	for i := 0; i < 2; i++ {
		if _, _, err := as.boardProvider(repo).ListOpenItems(ctx); err != nil {
			t.Fatalf("board read %d through the shared cache: %v", i+1, err)
		}
	}
	if n := boardReads.Load(); n != 2 {
		t.Errorf("board reads = %d after two builds through the shared cache, want 2 (one cold read)", n)
	}
}
