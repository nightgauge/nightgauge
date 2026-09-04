package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// The operator steer must land in the root the RUN reads (#1407).
//
// `runPipeline` resolves its root per repo (`resolveRunRoot(item.Repo)`) and
// reads `feedback-{N}.json` from there. The steer WRITER closed over the
// daemon's launch root instead, so on a multi-repo workspace the note was
// written to a directory the run never opens — and the card resolved
// successfully, so nothing surfaced it. The same subsystem's trace listener had
// resolved the per-repo root since it was written, 1,050 lines away.

// TestSteerRootFor_PrefersThePerRepoRunRoot pins the resolution the writer now
// shares with the trace listener.
func TestSteerRootFor_PrefersThePerRepoRunRoot(t *testing.T) {
	launch := t.TempDir()
	perRepo := t.TempDir()

	as := &AutonomousScheduler{workspaceRoot: launch}

	// No scheduler wired: nothing can resolve a per-repo root, so the launch
	// root stands. This is the single-repo case and must not regress.
	if got := as.steerRootFor("octocat/acme"); got != launch {
		t.Errorf("with no scheduler, root = %q, want the launch root %q", got, launch)
	}

	// With a scheduler that resolves the repo, the per-repo root wins.
	sched := &Scheduler{execMgr: execution.NewManager(launch, nil), launchRepo: "octocat/launch"}
	sched.WithRepoPathResolver(func(repo string) string {
		if repo == "octocat/acme" {
			return perRepo
		}
		return ""
	})
	as.scheduler = sched
	if got := as.steerRootFor("octocat/acme"); got != perRepo {
		t.Errorf("root = %q, want the per-repo run root %q — a steer written to the launch "+
			"root is never read by the run", got, perRepo)
	}
}

// TestWriteOperatorSteer_RefusesARunlessCard is the second rider.
//
// feedback-{N}.json is keyed by issue, so a card with an empty Context — the
// work-exhaustion producer raises one — wrote `feedback-0.json`, a file no run
// ever reads. This workspace already holds 20+ such cards, at least one
// resolved.
func TestWriteOperatorSteer_RefusesARunlessCard(t *testing.T) {
	root := t.TempDir()

	for _, issue := range []int{0, -1} {
		if err := WriteOperatorSteer(root, issue, "please retry", "feature-dev"); err == nil {
			t.Errorf("WriteOperatorSteer accepted issue %d — that writes feedback-%d.json, "+
				"which no run reads", issue, issue)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "pipeline", "feedback-0.json")); err == nil {
		t.Error("feedback-0.json was written despite the refusal")
	}

	// A real issue still writes, so the guard is not a blanket refusal.
	if err := WriteOperatorSteer(root, 7, "please retry", "feature-dev"); err != nil {
		t.Fatalf("WriteOperatorSteer(7): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "pipeline", "feedback-7.json")); err != nil {
		t.Errorf("feedback-7.json was not written: %v", err)
	}
}
