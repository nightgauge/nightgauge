package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/state"

	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// A run-state record another issue left behind is not adopted (#3081).
func TestAdoptableRunStateID_RefusesAnotherIssuesRecord(t *testing.T) {
	t.Parallel()

	id, err := newRunID()
	if err != nil {
		t.Fatalf("newRunID: %v", err)
	}
	rs := &runstate.RunState{RunID: id, IssueNumber: 1}

	if got, why := adoptableRunStateID(rs, 2); got != "" || why == "" {
		t.Fatalf("issue 2 adopted issue 1's run id: got %q why %q", got, why)
	}
	if got, why := adoptableRunStateID(rs, 1); got != id || why != "" {
		t.Fatalf("issue 1 did not adopt its own run id: got %q why %q", got, why)
	}
}

func TestAdoptableRunStateID_RefusesNonIdentity(t *testing.T) {
	t.Parallel()

	rs := &runstate.RunState{RunID: "not-a-uuid", IssueNumber: 3}
	if got, why := adoptableRunStateID(rs, 3); got != "" || why == "" {
		t.Fatalf("non-identity run id adopted: got %q why %q", got, why)
	}
	if got, why := adoptableRunStateID(nil, 3); got != "" || why != "" {
		t.Fatalf("nil record: got %q why %q", got, why)
	}
}

// A stale dev-N.json is gone, and kept as an archive, once feature-dev starts.
func TestArchivePriorDevContext_MovesStaleHandoff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dev := filepath.Join(dir, "dev-42.json")
	if err := os.WriteFile(dev, []byte(`{"issue_number":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)

	archived, err := archivePriorDevContext(dev, now)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := os.Stat(dev); !os.IsNotExist(err) {
		t.Fatalf("dev-42.json still present after archive: %v", err)
	}
	want := filepath.Join(dir, "dev-42.1700000000.prior.json")
	if archived != want {
		t.Fatalf("archive path = %q, want %q", archived, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != `{"issue_number":42}` {
		t.Fatalf("archive content = %q, %v", b, err)
	}

	// Nothing to archive is not an error.
	if got, err := archivePriorDevContext(dev, now); got != "" || err != nil {
		t.Fatalf("second archive: %q, %v", got, err)
	}
}

func TestStartsPastFeatureDev(t *testing.T) {
	t.Parallel()

	stages := []state.PipelineStage{state.StageIssuePickup, state.StageFeaturePlanning,
		state.StageFeatureDev, state.StageFeatureValidate}
	cases := map[int]bool{0: false, 2: false, 3: true}
	for idx, want := range cases {
		if got := startsPastFeatureDev(stages, idx); got != want {
			t.Errorf("startsPastFeatureDev(start=%d) = %v, want %v", idx, got, want)
		}
	}
	if startsPastFeatureDev([]state.PipelineStage{state.StageFeatureValidate}, 0) {
		t.Error("a stage list without feature-dev must not report past it")
	}
}
