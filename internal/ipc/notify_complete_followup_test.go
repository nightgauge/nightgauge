package ipc

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// A failed extension-path run files its discovered work at notifyComplete
// (#3685) instead of leaving the capture as an NGD050 orphan.
func TestNotifyComplete_FailedRunMaterializesDiscoveredWork(t *testing.T) {
	calls, left := notifyCompleteWithCapture(t, false)
	if calls != 1 || left != 0 {
		t.Fatalf("factory calls = %d, records left = %d; want 1 and 0", calls, left)
	}
}

// A successful run leaves its capture to the post-merge stage.
func TestNotifyComplete_SuccessfulRunLeavesCapture(t *testing.T) {
	calls, left := notifyCompleteWithCapture(t, true)
	if calls != 0 || left != 1 {
		t.Fatalf("factory calls = %d, records left = %d; want 0 and 1", calls, left)
	}
}

func notifyCompleteWithCapture(t *testing.T, success bool) (calls, left int) {
	t.Helper()
	dir := layouttest.Repo(t)
	s := NewServer(nil, WithWorkspaceRoot(dir))
	s.runEndFilerFactory = func(root, repo string, issue int) followup.FilerFactory {
		return func(context.Context, filing.Gate) (followup.Filer, bool, error) {
			calls++
			return nil, false, nil // report mode reads nothing
		}
	}
	stateDir, err := state.PipelineStateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := discoveredwork.Record{Summary: "Waiter ignores the ceiling", Evidence: []string{"e"}, SourceStage: "feature-dev",
		IssueNumber: 3685, SuggestedType: "bug", SuggestedPriority: "low", SuggestedSize: "XS"}
	if _, _, err := discoveredwork.Append(stateDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	const runID = "019001c1-0000-7000-8000-000000003685"
	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3685,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	if _, err := s.methods["pipeline.notifyComplete"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3685,"success":`+strconv.FormatBool(success)+`,"totalDurationMs":5,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	c, err := discoveredwork.Load(stateDir, 3685)
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		left = len(c.Records)
	}
	return calls, left
}
