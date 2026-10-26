package orchestrator

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// The board-status-write-failed card (#1501) reports one failed extension
// board write. It is informational: nothing the run does depends on the board,
// so the card must not block anything or bind a verb.
func TestBoardStatusWriteFailedIsAnInformationalEvent(t *testing.T) {
	r := BuildBoardStatusWriteFailed("octocat/acme", 42, "run-1", "feature-dev")

	if r.Producer != ProducerBoardStatusWriteFailed {
		t.Errorf("producer = %q", r.Producer)
	}
	if want := "board-status-write-failed:octocat/acme#42"; r.IdempotencyKey != want {
		t.Errorf("idempotency key = %q, want %q", r.IdempotencyKey, want)
	}
	if r.Standing {
		t.Error("card is Standing; a one-shot raise has no scan to retract it")
	}
	if r.Severity != attention.SeverityFYI {
		t.Errorf("severity = %q, want fyi — the run is unaffected", r.Severity)
	}
	for _, o := range r.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q, want noop", o.ID, o.Verb)
		}
	}
	if r.Context.Stage != "feature-dev" || r.Context.Issue != 42 || r.Context.Repo != "octocat/acme" {
		t.Errorf("context = %+v", r.Context)
	}
	for _, want := range []string{"feature-dev", "nightgauge doctor", "project_fields",
		"nightgauge project resolve --repo octocat/acme"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body does not mention %q", want)
		}
	}
}

func TestBoardStatusWriteFailedNamesAnUnknownStage(t *testing.T) {
	r := BuildBoardStatusWriteFailed("octocat/acme", 42, "", "")
	if r.Context.Stage != "unknown" {
		t.Errorf("stage = %q, want unknown", r.Context.Stage)
	}
}
