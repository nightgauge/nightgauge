package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// A throttle that arrives while the auto-scheduler reads the board holds the
// item the read picked: it is not dispatched, and waits for a later poll
// (#2352).
func TestRunAuto_HoldsTheItemWhenTheThrottleArrivesDuringTheBoardRead(t *testing.T) {
	throttle := NewDispatchThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The platform throttles the workspace while the board is read.
		throttle.Set(&platform.WorkspaceThrottle{MaxConcurrent: 0}, true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"organization":{"projectV2":{"id":"PVT_1","title":"Board","items":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{"id":"ITEM_2","content":{"__typename":"Issue","number":2,"title":"Owner's issue",
				"state":"OPEN","url":"https://github.com/o/r/issues/2","createdAt":"2026-01-01T00:00:00Z",
				"updatedAt":"2026-01-01T00:00:00Z","authorAssociation":"OWNER","labels":{"nodes":[]},
				"repository":{"nameWithOwner":"o/r"},"subIssues":{"nodes":[]},"blockedBy":{"nodes":[]},
				"blocking":{"nodes":[]},"parent":{"number":0,"title":""}},"fieldValues":{"nodes":[]}}]}}}}}`))
	}))
	defer srv.Close()
	logs := withCapturedLog(t)

	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{client: client, boardSvc: gh.NewBoardService(client, "o", 1), repoRunning: map[string]int{}}
	s.SetDispatchThrottle(throttle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunAuto(ctx, time.Hour) }()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logs.String(), "the workspace throttle holds dispatch; #2 waits") {
		if strings.Contains(logs.String(), "dispatching #2") {
			t.Fatalf("dispatched #2 under a throttle that arrived during the board read: %s", logs.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first poll never finished: %s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
