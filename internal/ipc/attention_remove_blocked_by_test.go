package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// #3081: a dependency-cycle card's per-edge option names the edge it removes,
// and the executor reads exactly those keys — after the option has been
// persisted as JSON, as the store does.
func TestRemoveBlockedByTargets_ReadsTheCycleCardsEdge(t *testing.T) {
	opt, ok := orchestrator.DependencyCycleEdgeOption(1, "o/a#2", "o/b#3")
	if !ok {
		t.Fatal("edge option not built")
	}
	raw, err := json.Marshal(opt)
	if err != nil {
		t.Fatal(err)
	}
	var persisted attention.Option
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}

	// The card's own Context is the cycle's first issue, not this edge's.
	blocked, blocker, err := removeBlockedByTargets("o/a", 1, persisted.Args, []string{"o/a", "o/b"})
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if want := (forge.IssueRef{Owner: "o", Repo: "a", Number: 2}); blocked != want {
		t.Fatalf("blocked = %+v, want %+v", blocked, want)
	}
	if want := (forge.IssueRef{Owner: "o", Repo: "b", Number: 3}); blocker != want {
		t.Fatalf("blocker = %+v, want %+v", blocker, want)
	}

	// A repository outside the configured set is refused.
	if _, _, err := removeBlockedByTargets("o/a", 1, persisted.Args, []string{"o/a"}); !errors.Is(err, attention.ErrVerbTargetNotConfigured) {
		t.Fatalf("unconfigured blocker repo: err = %v, want ErrVerbTargetNotConfigured", err)
	}
}

func TestRemoveBlockedByTargets_DefaultsToTheCardsIssue(t *testing.T) {
	blocked, blocker, err := removeBlockedByTargets("o/a", 5, map[string]any{"blockerNumber": float64(9)}, nil)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if blocked != (forge.IssueRef{Owner: "o", Repo: "a", Number: 5}) || blocker != (forge.IssueRef{Owner: "o", Repo: "a", Number: 9}) {
		t.Fatalf("targets = %+v blocked by %+v", blocked, blocker)
	}
	if _, _, err := removeBlockedByTargets("o/a", 5, map[string]any{}, nil); err == nil {
		t.Fatal("no blockerNumber: want an error")
	}
}

// The post-merge re-run arm refuses rather than resolving the card when the
// daemon cannot run it.
func TestExecuteVerb_RerunPostMergeRefusesWithoutTheCapability(t *testing.T) {
	s := NewServer(nil, WithWorkspaceRoot(layouttest.Repo(t)))
	req := &attention.DecisionRequest{ID: "r1", Context: attention.Context{Repo: "o/a", Issue: 21}}
	opt := attention.Option{ID: "rerun-post-merge", Verb: attention.VerbIssueRerunPostMerge}
	if err := s.ExecuteVerb(context.Background(), req, opt); err == nil {
		t.Fatal("issue.rerunPostMerge with no scheduler and no configured repo succeeded")
	}
}
