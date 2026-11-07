package orchestrator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Startup recovery must not hand a closed issue back to the dispatcher (#2750).
// The fake refuses every board write, so the log names which move was tried and
// writes proves whether any was.
func TestRecoverOrphanedRunningItems_ClosedIssue(t *testing.T) {
	cases := []struct {
		name        string
		issue       string // canned issue read; "" fails the read
		wantLog     string
		denyLog     []string
		wantWrite   bool
		wantRecheck bool
	}{
		{
			name:      "closed issue moves to Done, never Ready",
			issue:     issueStateJSON("CLOSED"),
			wantLog:   "the issue is closed; moving it to Done",
			denyLog:   []string{"back to Ready", "→ Ready"},
			wantWrite: true,
		},
		{
			name:      "open issue still returns to Ready",
			issue:     issueStateJSON("OPEN"),
			wantLog:   "back to Ready",
			denyLog:   []string{"moving it to Done"},
			wantWrite: true,
		},
		{
			name:        "unread issue state leaves the status unchanged",
			wantLog:     "could not read the issue's state",
			denyLog:     []string{"back to Ready", "→ Ready", "moving it to Done"},
			wantRecheck: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withCapturedLog(t)
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body := string(raw)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case isIssueRead(body) && tc.issue != "":
					_, _ = io.WriteString(w, tc.issue)
				case isIssueRead(body):
					_, _ = io.WriteString(w, `{"errors":[{"message":"issue read unavailable"}]}`)
				case strings.Contains(body, "pullRequests("):
					_, _ = io.WriteString(w, openPRPageJSON(nil, false, ""))
				default:
					writes.Add(1)
					_, _ = io.WriteString(w, `{"errors":[{"message":"forced failure"}]}`)
				}
			}))
			t.Cleanup(srv.Close)
			repos := []depgraph.RepoConfig{{Owner: "O", Name: "a", Project: 7}}
			as := NewAutonomousScheduler(nil, gh.NewClientWithURL("test-token", srv.URL), repos, nil,
				DefaultAutonomousConfig(), layouttest.Repo(t))
			as.trustGateOverride = allWritersGate()

			as.recoverOrphanedRunningItems(context.Background(), []RunningItem{{Repo: "O/a", Number: 1}})

			got := buf.String()
			if !strings.Contains(got, tc.wantLog) {
				t.Errorf("log lacks %q: %q", tc.wantLog, got)
			}
			for _, deny := range tc.denyLog {
				if strings.Contains(got, deny) {
					t.Errorf("log has %q: %q", deny, got)
				}
			}
			if wrote := writes.Load() > 0; wrote != tc.wantWrite {
				t.Errorf("board write attempted = %v, want %v", wrote, tc.wantWrite)
			}
			if _, queued := pendingRecheck(as, "O/a", 1); queued != tc.wantRecheck {
				t.Errorf("recheck queued = %v, want %v", queued, tc.wantRecheck)
			}
		})
	}
}

// A recheck ends in a revert that can write Ready, so it reads the issue first:
// the entry recovery queues for an unread issue must not put it in Ready once
// the forge answers that the issue is closed (#2750).
func TestRecheckPR_ClosedIssueNeverRevertsToReady(t *testing.T) {
	cases := []struct {
		name      string
		issue     string
		wantLog   string
		denyLog   string
		wantEntry bool
	}{
		{name: "closed goes to Done", issue: issueStateJSON("CLOSED"), wantLog: "move-to-done:", denyLog: "Ready"},
		{name: "open still reverts", issue: issueStateJSON("OPEN"), wantLog: "Ready", denyLog: "move-to-done:"},
		{name: "unread keeps the entry", wantLog: "still unreadable", denyLog: "Ready", wantEntry: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withCapturedLog(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body := string(raw)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case isIssueRead(body) && tc.issue != "":
					_, _ = io.WriteString(w, tc.issue)
				case strings.Contains(body, "pullRequests("):
					_, _ = io.WriteString(w, openPRPageJSON(nil, false, ""))
				default:
					_, _ = io.WriteString(w, `{"errors":[{"message":"forced failure"}]}`)
				}
			}))
			t.Cleanup(srv.Close)
			repos := []depgraph.RepoConfig{{Owner: "O", Name: "a", Project: 7}}
			as := NewAutonomousScheduler(nil, gh.NewClientWithURL("test-token", srv.URL), repos, nil,
				DefaultAutonomousConfig(), layouttest.Repo(t))
			as.trustGateOverride = allWritersGate()
			as.enqueuePRRecheck("O/a", 1, 0, "")

			as.reconcilePRRechecks(context.Background())

			got := buf.String()
			if !strings.Contains(got, tc.wantLog) {
				t.Errorf("log lacks %q: %q", tc.wantLog, got)
			}
			if strings.Contains(got, tc.denyLog) {
				t.Errorf("log has %q: %q", tc.denyLog, got)
			}
			if _, queued := pendingRecheck(as, "O/a", 1); queued != tc.wantEntry {
				t.Errorf("entry queued = %v, want %v", queued, tc.wantEntry)
			}
		})
	}
}
