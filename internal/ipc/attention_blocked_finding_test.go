package ipc

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TestClearBlockedFindingRefusesUnresolvedRoot: with no absolute repository
// root, the finding's path cannot be resolved, so clearing must fail rather
// than report "already gone" and resolve a card whose hold may still be on
// disk (#2033).
func TestClearBlockedFindingRefusesUnresolvedRoot(t *testing.T) {
	for _, root := range []string{"", "relative/repo"} {
		c := blockedFindingClearer{server: &Server{workspaceRoot: root}}
		if err := c.ClearBlockedFinding(context.Background(), "o/r", 7); err == nil {
			t.Errorf("root %q: ClearBlockedFinding = nil, want an error", root)
		}
	}
}

// TestClearBlockedFindingRemovesAndToleratesAbsent: with an absolute root the
// file is removed, and a second clear of the now-absent file still succeeds.
func TestClearBlockedFindingRemovesAndToleratesAbsent(t *testing.T) {
	root := layouttest.Repo(t)
	path := orchestrator.BlockedFindingPath(root, 7)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := blockedFindingClearer{server: &Server{workspaceRoot: root}}
	for i := 0; i < 2; i++ {
		if err := c.ClearBlockedFinding(context.Background(), "o/r", 7); err != nil {
			t.Fatalf("clear #%d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("finding still present after clear: %v", err)
	}
}

// TestBlockedResolvedReleasesSchedulerHold drives the real out-of-scope card's
// "cleared" option through ExecuteVerb (#3170). The run's Failed entry is held
// for HoldOperatorResume; the click must delete the finding AND release that
// hold. When the delete fails, the hold must stay, so the card survives with
// the issue still parked.
func TestBlockedResolvedReleasesSchedulerHold(t *testing.T) {
	setup := func(t *testing.T) (string, *orchestrator.AutonomousScheduler) {
		root := layouttest.Repo(t)
		cfgDir := filepath.Join(root, ".nightgauge")
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "project:\n  owner: o\n  number: 3\n  repo: r\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		statePath, err := orchestrator.AutonomousStatePath(root)
		if err != nil {
			t.Fatal(err)
		}
		seed := orchestrator.AutonomousState{Failed: []orchestrator.FailedItem{{
			Repo: "o/r", Number: 7, Kind: orchestrator.TerminalKindNotPipelineActionable, AttemptCount: 1,
		}}}
		b, _ := json.Marshal(seed)
		if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, b, 0o644); err != nil {
			t.Fatal(err)
		}
		as := orchestrator.NewAutonomousScheduler(nil, nil,
			[]depgraph.RepoConfig{{Owner: "o", Name: "r", Project: 3}}, nil,
			orchestrator.DefaultAutonomousConfig(), root)
		t.Cleanup(func() { as.Shutdown(time.Second) })
		return root, as
	}
	held := func(as *orchestrator.AutonomousScheduler) bool {
		for _, f := range as.Status().Failed {
			if f.Repo == "o/r" && f.Number == 7 {
				return true
			}
		}
		return false
	}

	t.Run("clear releases the hold", func(t *testing.T) {
		root, as := setup(t)
		path := orchestrator.BlockedFindingPath(root, 7)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{autonomousScheduler: as, writer: io.Discard, workspaceRoot: root}
		req := orchestrator.BuildOutOfScopeBlocker("o/r", 7, "run-1", "feature-planning")
		if !held(as) {
			t.Fatal("precondition: o/r#7 must be held")
		}
		if err := s.ExecuteVerb(context.Background(), &req, *req.FindOption("cleared")); err != nil {
			t.Fatalf("ExecuteVerb(cleared): %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("finding still on disk: %v", err)
		}
		if held(as) {
			t.Fatal("o/r#7 still held after Blocker resolved — the issue stays skipped forever (#3170)")
		}
	})

	t.Run("failed delete keeps the hold", func(t *testing.T) {
		root, as := setup(t)
		path := orchestrator.BlockedFindingPath(root, 7)
		// A non-empty directory at the finding's path: os.Remove fails with a
		// real error, not not-exist.
		if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		s := &Server{autonomousScheduler: as, writer: io.Discard, workspaceRoot: root}
		req := orchestrator.BuildOutOfScopeBlocker("o/r", 7, "run-1", "feature-planning")
		if err := s.ExecuteVerb(context.Background(), &req, *req.FindOption("cleared")); err == nil {
			t.Fatal("ExecuteVerb succeeded though the finding could not be deleted")
		}
		if !held(as) {
			t.Fatal("hold released although the finding is still on disk")
		}
	})
}
