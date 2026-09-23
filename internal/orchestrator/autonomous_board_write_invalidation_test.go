package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestSchedulerStatusMovesInvalidateSharedBoardCache pins that every board
// write the scheduler issues goes through boardcache.WrapProject, so the
// daemon's shared open snapshot (served to board.listOpen, board.counts and
// the sweeps) is dropped when the scheduler moves an issue. Before, these
// writes used a bare gh.ProjectService: the scheduler read boards through the
// cache but wrote around it, and the daemon kept serving the pre-move snapshot
// for up to a TTL.
//
// The forge answers every request with a GraphQL error. WrapProject
// invalidates on failure as well as success (a failed write may still have
// landed), so the test needs no MoveStatus fixture, only proof that the write
// was attempted through the wrapped service.
func TestSchedulerStatusMovesInvalidateSharedBoardCache(t *testing.T) {
	moves := map[string]func(as *AutonomousScheduler, ctx context.Context){
		"revertFailedIssueStatus": func(as *AutonomousScheduler, ctx context.Context) {
			as.revertFailedIssueStatus(ctx, "O/a", 1)
		},
		"moveIssueToDone": func(as *AutonomousScheduler, ctx context.Context) {
			as.moveIssueToDone(ctx, "O/a", 1)
		},
		"moveIssueToInReview": func(as *AutonomousScheduler, ctx context.Context) {
			as.moveIssueToInReview(ctx, "O/a", 1)
		},
		"moveIssueToInProgress": func(as *AutonomousScheduler, ctx context.Context) {
			as.moveIssueToInProgress(ctx, "O/a", 1, "test")
		},
		"recoverOrphanedRunningItems": func(as *AutonomousScheduler, ctx context.Context) {
			as.recoverOrphanedRunningItems(ctx, []RunningItem{{Repo: "O/a", Number: 1}})
		},
	}

	for name, move := range moves {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"errors":[{"message":"forced failure"}]}`))
			}))
			defer srv.Close()

			repos := []depgraph.RepoConfig{{Owner: "O", Name: "a", Project: 7}}
			as := NewAutonomousScheduler(nil, gh.NewClientWithURL("test-token", srv.URL), repos, nil, DefaultAutonomousConfig(), t.TempDir())
			cache := boardcache.New(0)
			as.SetBoardCache(cache)

			// Seed the board's open snapshot, as a sweep or board.listOpen would.
			ctx := context.Background()
			board := &countingBoard{}
			if _, _, err := cache.Wrap(board, "O", 7).ListOpenItems(ctx); err != nil {
				t.Fatalf("seed read: %v", err)
			}
			if _, ok := cache.Peek("O", 7, "open"); !ok {
				t.Fatalf("seed: open snapshot not cached")
			}

			move(as, ctx)

			if requests.Load() == 0 {
				t.Fatalf("%s issued no forge request; the write was never attempted", name)
			}
			if _, ok := cache.Peek("O", 7, "open"); ok {
				t.Errorf("%s left the board's open snapshot in the shared cache; the daemon would serve the pre-move board", name)
			}
		})
	}
}

// TestSchedulerWithoutBoardCacheStillWrites pins the CLI shape: no shared
// cache set, and the board write still goes out, unwrapped.
func TestSchedulerWithoutBoardCacheStillWrites(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"forced failure"}]}`))
	}))
	defer srv.Close()

	repos := []depgraph.RepoConfig{{Owner: "O", Name: "a", Project: 7}}
	as := NewAutonomousScheduler(nil, gh.NewClientWithURL("test-token", srv.URL), repos, nil, DefaultAutonomousConfig(), t.TempDir())
	as.revertFailedIssueStatus(context.Background(), "O/a", 1)
	if requests.Load() == 0 {
		t.Fatal("no forge request without a board cache")
	}
}

// TestSchedulerBoardWritesHaveOneConstructor keeps the property above from
// eroding: a new status move that builds its own gh.ProjectService would write
// around the shared cache again. The only construction allowed in the
// autonomous scheduler's files is the one inside projectService.
//
// Scoped to autonomous*.go: the per-run Scheduler (epic.go's post-merge board
// sync) holds no board cache to invalidate and is not covered here.
func TestSchedulerBoardWritesHaveOneConstructor(t *testing.T) {
	files, err := filepath.Glob("autonomous*.go")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(src), "gh.NewProjectService("); n > 0 {
			total += n
			if f != "autonomous.go" {
				t.Errorf("%s constructs gh.NewProjectService directly %d time(s); use as.projectService so the write invalidates the board cache", f, n)
			}
		}
	}
	if total != 1 {
		t.Errorf("found %d gh.NewProjectService constructions, want exactly 1 (inside projectService)", total)
	}
}
