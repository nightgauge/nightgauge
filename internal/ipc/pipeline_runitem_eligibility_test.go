package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// readAuthorForge says every issue is by "outsider", who has read access.
type readAuthorForge struct{}

func (readAuthorForge) RepoPermission(_ context.Context, _, _, login string) (trust.Level, error) {
	if login == "outsider" {
		return trust.LevelRead, nil
	}
	return "", errors.New("REST 404")
}

func (readAuthorForge) IssueSignals(context.Context, string, string, int) (trust.Signals, error) {
	return trust.Signals{Author: "outsider"}, nil
}

// pipeline.runItem refuses an issue no user with write access vouched for,
// with an error naming the author's permission level and how to approve it,
// and starts no run (#2130).
func TestPipelineRunItem_RefusesAnIneligibleIssue(t *testing.T) {
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: t.TempDir()})
	sched.SetTrustGate(trust.NewGate(readAuthorForge{}, nil))
	s := NewServer(nil, WithScheduler(sched))

	handler, ok := s.methods["pipeline.runItem"]
	if !ok {
		t.Fatal("pipeline.runItem is not registered")
	}
	params, _ := json.Marshal(map[string]interface{}{"owner": "acme", "repo": "web", "issueNumber": 7, "title": "x"})
	res, err := handler(context.Background(), params)
	if err == nil {
		t.Fatalf("pipeline.runItem queued an ineligible issue: %v", res)
	}
	for _, want := range []string{"@outsider", "read access", "pipeline:approved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// The handler returns before it hands the item to the scheduler, so no
	// executionId is minted and nothing is queued.
	if res != nil {
		t.Fatalf("refusal carried a result %v, want none", res)
	}
	if got := sched.QueueList(); len(got) != 0 {
		t.Fatalf("queue holds %d item(s) after a refusal, want 0", len(got))
	}
}

// writerForge vouches for every issue: its author has write access. For
// tests about something other than eligibility.
type writerForge struct{}

func (writerForge) RepoPermission(context.Context, string, string, string) (trust.Level, error) {
	return trust.LevelWrite, nil
}

func (writerForge) IssueSignals(context.Context, string, string, int) (trust.Signals, error) {
	return trust.Signals{Author: "maintainer"}, nil
}

// permissiveGate passes every issue.
func permissiveGate() *trust.Gate { return trust.NewGate(writerForge{}, nil) }
