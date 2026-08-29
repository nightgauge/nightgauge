package orchestrator

// #440 — the Go dispatch path's terminal latch and seal.
//
// ADR-017 Decision 5 specifies a two-half terminal contract: latch the durable
// `terminal` marker at outcome determination, then seal and remove the
// snapshot after cleanup. Until #440 BOTH halves were extension-path-only —
// `internal/ipc`'s ClaimTerminal and SealAndRemove — and the Go scheduler's
// terminal defer had neither. The probe in the issue was literal:
//
//	$ grep -rn "MarkTerminal(\|SealAndRemove(" internal/orchestrator
//	(no matches)
//
// The consequence was not cosmetic. A finished or crashed Go-scheduler run
// left a NON-TERMINAL snapshot on disk indefinitely, collected only by the
// orphan reconciler's 14-day cap, so the two dispatch paths silently disagreed
// about what a snapshot on disk means — and every reader had to special-case
// it (#410's ActiveIssuesFromSnapshots is liveness-bounded, not
// terminality-based, for precisely this reason).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestRunPipeline_LatchesTerminalAndSealsTheSnapshot is the direct
// reproduction-and-fix pin for #440: mid-run there is a live non-terminal
// snapshot; after the run the completion callback sees `terminal: true` and
// the file is GONE.
func TestRunPipeline_LatchesTerminalAndSealsTheSnapshot(t *testing.T) {
	root := gitWorkspace(t)
	runner := &runIDCapturingRunner{}
	s := newRunIdentityTestScheduler(t, root, runner)

	item := types.BoardItem{Number: 8810, Repo: "nightgauge/nightgauge", ID: "item-8810", Title: "terminal latch"}
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")

	// Mid-run the snapshot must exist and must NOT be terminal — otherwise the
	// post-run assertions below would pass against a run that never wrote one,
	// which is the way this test could go quietly vacuous.
	//
	// The LIVE runtime pointer is captured here too, and deliberately: the
	// onPipelineComplete callback is handed runtime.Snapshot() — a COPY taken
	// before the latch fires — so asserting the latch against it would assert
	// nothing about the run. LookupRunByID returns the object the defer
	// actually mutates, for as long as it is registered.
	//
	// Resolved by the identity the snapshot itself carries (#379). The old
	// issue-number lookup would have been satisfied by ANY run of this issue;
	// this can only be satisfied by the run that wrote the snapshot the
	// assertions below read.
	var midRunSeen, midRunTerminal bool
	var live *state.RuntimeState
	runner.onStage = func() {
		if midRunSeen {
			return
		}
		found, err := state.FindPersistedStatesForIssue(stateDir, item.Number)
		if err != nil || len(found) != 1 {
			return
		}
		midRunSeen = true
		midRunTerminal = found[0].Terminal
		live = s.LookupRunByID(found[0].RunID)
	}

	s.runPipeline(context.Background(), item)

	if !midRunSeen {
		t.Fatal("no snapshot was observed mid-run — the test cannot distinguish a sealed run from one that never persisted")
	}
	if midRunTerminal {
		t.Error("the mid-run snapshot is already terminal; the latch must fire at outcome determination, not before")
	}
	if live == nil {
		t.Fatal("the live runtime was never registered — nothing to assert the latch against")
	}

	// HALF ONE — the durable latch, read off the live runtime the defer
	// latched: the same object every Persist after that point marshals from.
	snap := live.Snapshot()
	if !snap.Terminal {
		t.Error("the run was never latched terminal — ADR-017 Decision 5 step 1c did not run on the Go path (#440)")
	}
	if snap.TerminalAt == nil {
		t.Error("Terminal is set but TerminalAt is nil — the marker must carry its timestamp")
	}
	if snap.TerminalOutcome == "" {
		t.Error("the terminal latch recorded no outcome string")
	}

	// HALF TWO — the seal. The path is the identity, so this is the exact
	// file this run wrote and no other.
	if snap.RunID == "" {
		t.Fatal("the run carries no identity, so nothing can be said about which file was sealed")
	}
	sealed := filepath.Join(stateDir, state.SnapshotFilename(item.Number, snap.RunID))
	if _, err := os.Stat(sealed); !os.IsNotExist(err) {
		t.Errorf("the snapshot survived the run (stat: %v) — SealAndRemove did not run (#440)", err)
	}

	// And the seal latched: a later Persist writes nothing rather than
	// resurrecting the file the seal just removed (F27).
	if err := live.Persist(stateDir); err == nil {
		t.Error("a post-seal Persist succeeded; it must return ErrRunSealed without writing")
	}
	if _, err := os.Stat(sealed); !os.IsNotExist(err) {
		t.Error("a post-seal Persist RESURRECTED the sealed snapshot — the seal is not latched")
	}
}

// TestSchedulerTerminalOutcome_MatchesTheExtensionPathVocabulary pins the
// three outcome strings against the extension path's outcomeFor closure
// (internal/ipc/server.go, pipeline.notifyComplete). The marker is read by
// consumers that do not know which path produced the run, so a fourth
// spelling here would be a path-dependent vocabulary in a field whose whole
// purpose is to be path-independent.
func TestSchedulerTerminalOutcome_MatchesTheExtensionPathVocabulary(t *testing.T) {
	cases := []struct {
		name    string
		success bool
		kind    string
		want    string
	}{
		{"success is complete", true, "", "complete"},
		{"success wins over a stale kind", true, TerminalKindSubagentCrash, "complete"},
		{"a deferral is cancelled, not failed", false, TerminalKindBlockedDependency, "cancelled"},
		{"an unclassified failure is failed", false, "", "failed"},
		{"a classified failure is failed", false, TerminalKindNetworkUnavailable, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schedulerTerminalOutcome(tc.success, tc.kind); got != tc.want {
				t.Errorf("schedulerTerminalOutcome(%v, %q) = %q, want %q", tc.success, tc.kind, got, tc.want)
			}
		})
	}
}
