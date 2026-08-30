package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// #1148 — the halt on a terminal stage failure is scoped to the repository
// that produced it.
//
// Before this, `haltQueueOnSlotFailure` answered one issue's validation
// failure in one repo by calling `autonomousPause()`: Status went to "paused"
// and EVERY repository in the workspace stopped dispatching until a human
// clicked Resume. The blast radius was the whole multi-repo workspace; the
// evidence was one issue.
//
// These tests drive the real `runCycle` dispatch loop rather than the
// predicate, so they cover the wiring — delete the `repoPaused` continue in
// the loop and they go red, not merely the helper's unit test.

// newRepoHaltScheduler builds a scheduler whose graph offers one Ready issue
// in each of two repositories, recording every dispatch.
func newRepoHaltScheduler(t *testing.T) (*AutonomousScheduler, *[]string) {
	t.Helper()
	nodes := []*depgraph.Node{
		{Repo: "acme/web", Number: 10, Title: "web work", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 1},
		{Repo: "acme/api", Number: 20, Title: "api work", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 1},
	}
	graph := buildTestGraph(nodes, nil)

	dispatched := &[]string{}
	as := NewAutonomousScheduler(nil, nil,
		[]depgraph.RepoConfig{
			{Owner: "acme", Name: "web", Project: 7},
			{Owner: "acme", Name: "api", Project: 8},
		},
		nil, DefaultAutonomousConfig(), t.TempDir())
	as.state.Status = "running"
	as.config.MaxConcurrent = 5
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) { return graph, nil }
	as.onDispatch = func(owner, name string, number int, _ string) {
		*dispatched = append(*dispatched, repoHaltKey(owner, name, number))
	}
	t.Cleanup(as.drainBackground)
	return as, dispatched
}

func repoHaltKey(owner, name string, number int) string {
	return fmt.Sprintf("%s/%s#%d", owner, name, number)
}

func dispatchedRepoHalt(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// TestRepoHalt_StopsOnlyItsOwnRepository is the acceptance criterion stated
// directly: a halt raised against acme/web must not stop acme/api.
//
// BEHAVIOURAL RED-PROOF: restore the old global behaviour by deleting the
// `if haltedRepo { continue }` guard in runCycle's dispatch loop (or by making
// PauseRepo call Pause instead) and acme/web dispatches again — the first
// assertion fails. Restore the guard but widen it to `len(PausedRepos) > 0`
// (the fleet-wide reading) and the SECOND assertion fails instead: acme/api
// stops too. Neither neuter is a compile error.
func TestRepoHalt_StopsOnlyItsOwnRepository(t *testing.T) {
	stubReconcileGhUnreachable(t)
	as, dispatched := newRepoHaltScheduler(t)

	if !as.PauseRepo("acme/web", "issue #10 failed at feature-validate", haltTagSlotFailure, 10, "feature-validate") {
		t.Fatalf("PauseRepo returned false — nothing was halted")
	}

	as.runCycle(context.Background())
	as.drainBackground()

	if dispatchedRepoHalt(*dispatched, "acme/web#10") {
		t.Errorf("halted repository acme/web still dispatched: %v — the halt does not hold", *dispatched)
	}
	if !dispatchedRepoHalt(*dispatched, "acme/api#20") {
		t.Errorf("acme/api did not dispatch: %v — one repository's failure still stops the whole workspace", *dispatched)
	}
}

// TestRepoHalt_RequiresAnExplicitResume pins that this narrows the blast
// radius WITHOUT removing the gate: the halted repo stays halted across
// cycles, and only an explicit ResumeRepo releases it.
//
// BEHAVIOURAL RED-PROOF: make ResumeRepo a no-op that returns true, or give
// the halt any self-clearing expiry, and the post-resume assertion fails. Make
// PauseRepo non-persistent / cleared at the top of runCycle and the
// "still halted on the second cycle" assertion fails.
func TestRepoHalt_RequiresAnExplicitResume(t *testing.T) {
	stubReconcileGhUnreachable(t)
	as, dispatched := newRepoHaltScheduler(t)
	as.PauseRepo("acme/web", "issue #10 failed at feature-validate", haltTagSlotFailure, 10, "feature-validate")

	// Several cycles pass. Nothing auto-recovers a machine halt.
	as.runCycle(context.Background())
	as.runCycle(context.Background())
	as.drainBackground()
	if dispatchedRepoHalt(*dispatched, "acme/web#10") {
		t.Fatalf("acme/web dispatched without a Resume: %v — the human gate is gone", *dispatched)
	}
	if !as.RepoPaused("acme/web") {
		t.Fatalf("halt cleared itself between cycles")
	}

	if !as.ResumeRepo("acme/web") {
		t.Fatalf("ResumeRepo reported nothing to resume while the repo was halted")
	}
	if as.RepoPaused("acme/web") {
		t.Fatalf("repo still reads halted after ResumeRepo")
	}

	as.runCycle(context.Background())
	as.drainBackground()
	if !dispatchedRepoHalt(*dispatched, "acme/web#10") {
		t.Errorf("acme/web did not dispatch after an explicit Resume: %v", *dispatched)
	}
}

// TestRepoHalt_FleetResumeReleasesEveryRepo pins that the fleet-wide Resume
// still means "go again", everywhere. A repo-scoped halt leaves Status
// "running", so a status-keyed Resume guard would make the operator's Resume
// button a silent no-op over it.
//
// BEHAVIOURAL RED-PROOF: drop `len(as.state.PausedRepos) > 0` from Resume's
// guard (restoring the status-only test) and this goes red.
func TestRepoHalt_FleetResumeReleasesEveryRepo(t *testing.T) {
	as, _ := newRepoHaltScheduler(t)
	as.PauseRepo("acme/web", "boom", haltTagSlotFailure, 10, "feature-validate")
	as.PauseRepo("acme/api", "boom", haltTagSlotFailure, 20, "feature-validate")

	if as.state.Status != "running" {
		t.Fatalf("a repo-scoped halt changed fleet Status to %q — it must not", as.state.Status)
	}

	as.Resume()

	if as.RepoPaused("acme/web") || as.RepoPaused("acme/api") {
		t.Errorf("fleet Resume left repositories halted: %v", as.PausedReposSnapshot())
	}
}

// TestRepoHalt_ResumeIsScopedToOneRepo — resuming acme/web must not release
// acme/api. Two halts are two independent triage decisions.
func TestRepoHalt_ResumeIsScopedToOneRepo(t *testing.T) {
	as, _ := newRepoHaltScheduler(t)
	as.PauseRepo("acme/web", "boom", haltTagSlotFailure, 10, "feature-validate")
	as.PauseRepo("acme/api", "boom", haltTagSlotFailure, 20, "feature-validate")

	as.ResumeRepo("acme/web")

	if as.RepoPaused("acme/web") {
		t.Errorf("acme/web still halted after its own resume")
	}
	if !as.RepoPaused("acme/api") {
		t.Errorf("resuming acme/web also released acme/api — a resume must not decide for a repo nobody triaged")
	}
}

// TestRepoHalt_IsDiscoverable pins the snapshot the UI reads. A halted repo
// the operator cannot see is a halted repo that gets forgotten, which is the
// failure mode narrowing the halt would otherwise introduce.
func TestRepoHalt_IsDiscoverable(t *testing.T) {
	as, _ := newRepoHaltScheduler(t)
	as.PauseRepo("acme/web", "haltQueueOnSlotFailure: issue #10 failed at feature-validate",
		haltTagSlotFailure, 10, "feature-validate")

	snap := as.Status()
	rec, ok := snap.PausedRepos["acme/web"]
	if !ok || rec == nil {
		t.Fatalf("Status() does not report the halt: %+v", snap.PausedRepos)
	}
	if rec.Issue != 10 || rec.Stage != "feature-validate" {
		t.Errorf("halt record cannot say why it stopped: %+v", rec)
	}
	if rec.PausedAt == "" || rec.TriggeredBy != haltTagSlotFailure {
		t.Errorf("halt record missing provenance: %+v", rec)
	}

	// The snapshot must be a copy — a caller holding it must not observe (or
	// race with) later PauseRepo/ResumeRepo writes.
	as.ResumeRepo("acme/web")
	if _, still := snap.PausedRepos["acme/web"]; !still {
		t.Errorf("Status() aliased the live map — the snapshot mutated under the caller")
	}
}

// TestRepoHalt_KeepsTheTerminalFailureCardAlive: scoping the halt must not
// retract the card that names the failure. `anyRepoHaltedOnSlotFailure` is the
// conjunct that keeps it standing.
//
// BEHAVIOURAL RED-PROOF: remove the `|| anyRepoHaltedOnSlotFailure(...)` from
// reconcileTerminalFailureCards' `stillHalted` and this goes red — the card
// retracts on the first cycle after a repo-scoped halt.
func TestRepoHalt_KeepsTheTerminalFailureCardAlive(t *testing.T) {
	st := &AutonomousState{
		PausedRepos: map[string]*RepoPauseRecord{
			"acme/web": {Repo: "acme/web", TriggeredBy: haltTagSlotFailure},
		},
	}
	if haltedOnSlotFailure(st) {
		t.Fatalf("a repo-scoped halt must not latch the FLEET halt")
	}
	if !anyRepoHaltedOnSlotFailure(st) {
		t.Errorf("a repo halted on a slot failure does not read as halted — the card would retract itself")
	}

	// A repo halted for some other reason is not a terminal-failure halt.
	st.PausedRepos["acme/web"].TriggeredBy = "user"
	if anyRepoHaltedOnSlotFailure(st) {
		t.Errorf("a non-slot-failure repo halt was counted as a terminal-failure halt")
	}
}

// TestRepoHalt_RefusesAnUnattributedRepo. A dispatch whose repo identity is
// unknown cannot be scoped, and silently converting that into "halt nothing"
// would drop the gate entirely. PauseRepo refuses; the caller falls back to
// the fleet halt.
func TestRepoHalt_RefusesAnUnattributedRepo(t *testing.T) {
	as, _ := newRepoHaltScheduler(t)
	if as.PauseRepo("", "boom", haltTagSlotFailure, 10, "feature-validate") {
		t.Errorf("PauseRepo accepted an empty repo — an unattributable halt must not silently succeed")
	}
	if len(as.PausedReposSnapshot()) != 0 {
		t.Errorf("empty repo produced a halt record: %v", as.PausedReposSnapshot())
	}
}
