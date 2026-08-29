package doctor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// #1105. `doctor` had arms for leaked worktrees, stranded branches and
// pipeline stashes, and none for the one leak that leaves nothing on disk:
// work committed by a guard kill and anchored under refs/nightgauge/wip/.
// Every check read green while a killed stage's only surviving copy of an
// issue's deliverable sat unclaimed.

// preserveWip plants an anchor the way preserveWorkInProgress does: commit the
// dirty tree on the stage branch, then update-ref it outside refs/heads/.
func preserveWip(r *leakRepo, branch string, issue int, stage string, ts int64) string {
	r.t.Helper()
	r.git("checkout", "-q", "-b", branch, "main")
	r.write("unfinished.txt", "half-done\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", fmt.Sprintf(
		"wip(%s): preserve uncommitted work from a terminated stage\n\nRefs: #%d\nNightgauge-WIP: %s",
		stage, issue, stage))
	sha := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	ref := fmt.Sprintf("%s/%s-%d", reclaim.WipRefNamespace, strings.ReplaceAll(branch, "/", "-"), ts)
	r.git("update-ref", ref, sha)
	r.git("checkout", "-q", "main")
	return ref
}

func TestCheckPreservedWip_ReportsWorkFromAKilledStage(t *testing.T) {
	r := newLeakRepo(t)
	preserveWip(r, "feat/338-guest-auth", 338, "feature-validate", 1787939337)

	item, warning := checkPreservedWip(r.dir, time.Now())
	if item.OK {
		t.Fatal("doctor reported healthy with a killed stage's only copy of its work unclaimed")
	}
	if !strings.Contains(item.Error, "#338") {
		t.Errorf("the warning does not name the issue, so it cannot be matched to a re-run: %q", item.Error)
	}
	if !strings.Contains(item.Error, "nightgauge wip list") {
		t.Errorf("the warning names no remedy: %q", item.Error)
	}
	if warning == "" {
		t.Error("the arm produced no workspace warning, so nothing reaches doctor's Warnings line")
	}
	if !strings.Contains(item.Detail, "1 preserved WIP ref") {
		t.Errorf("detail = %q, want the count of preserved refs", item.Detail)
	}
}

func TestCheckPreservedWip_HealthyRepoPasses(t *testing.T) {
	r := newLeakRepo(t)
	item, warning := checkPreservedWip(r.dir, time.Now())
	if !item.OK {
		t.Fatalf("a repo with no preserved work must pass: %+v", item)
	}
	if warning != "" {
		t.Errorf("unexpected warning on a clean repo: %q", warning)
	}
}

// Pins the WIRING, not the classifier. Every test above passes while nothing
// in RunDoctor ever calls checkPreservedWip — which is the same "one writer,
// zero readers" shape one level up, and exactly how a WIP ref stayed
// unreported for a day. Asserted on presence rather than verdict: the row's OK
// depends on the machine the test runs on, its existence does not.
func TestRunDoctor_EmitsPreservedWipArm(t *testing.T) {
	result := RunDoctor(context.Background(), nil, nil, nil)

	if _, ok := result.Checks["preserved_wip"]; !ok {
		t.Fatalf("preserved_wip missing from doctor's checks; got %v", checkKeys(result))
	}
}

func TestCheckPreservedWip_NoRootsIsNeverHealthy(t *testing.T) {
	// A directory that is not a repo and not a workspace resolves no roots.
	// "I could not look" must never print as "there is nothing there" (#296).
	item, warning := checkPreservedWip(t.TempDir(), time.Now())
	if item.OK {
		t.Fatal("an unverifiable scan reported healthy")
	}
	if warning == "" {
		t.Error("an unverifiable scan produced no warning")
	}
}
