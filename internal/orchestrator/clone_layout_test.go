package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// autonomousStatePathT is the autonomous scheduler's state file for root
// (AutonomousStatePath), making root a git repository first when it is not
// one: the file lives in the checkout's per-checkout directory (ADR-024 § 7).
func autonomousStatePathT(t testing.TB, root string) string {
	t.Helper()
	return layouttest.CheckoutPath(t, root, AutonomousStateName)
}

// TestRunControlSingletonsArePerCheckout: the main checkout and a linked
// worktree of one clone share CLONE (the keyed pipeline state) but each has
// its own current-run.json, run-state.json, queue-state.json and autonomous
// state (ADR-024 § 7), so two orchestrators — one per checkout — never
// overwrite each other's run control.
func TestRunControlSingletonsArePerCheckout(t *testing.T) {
	mainRoot := layouttest.Repo(t)
	gittest.Run(t, mainRoot, "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(t.TempDir(), "wt")
	gittest.Run(t, mainRoot, "worktree", "add", "-q", "--detach", linked)

	if a, b := layouttest.PipelineDir(t, mainRoot), layouttest.PipelineDir(t, linked); a != b {
		t.Fatalf("the two checkouts do not share CLONE: %s vs %s", a, b)
	}
	for _, name := range []string{layout.CheckoutCurrentRun, layout.CheckoutRunState, layout.CheckoutQueueState, AutonomousStateName} {
		a, b := checkoutStatePath(mainRoot, name), checkoutStatePath(linked, name)
		if a == "" || b == "" || a == b {
			t.Errorf("%s: main %q, linked %q — want two distinct resolved paths", name, a, b)
		}
		if a != layouttest.CheckoutPath(t, mainRoot, name) {
			t.Errorf("%s: main checkout path %q is not in its CHECKOUT", name, a)
		}
	}

	// The sidecar each orchestrator writes is its own.
	if err := writeCurrentRunSidecar(mainRoot, CurrentRunSidecar{IssueNumber: 1, RunID: "main", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := writeCurrentRunSidecar(linked, CurrentRunSidecar{IssueNumber: 2, RunID: "linked", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for root, want := range map[string]int{mainRoot: 1, linked: 2} {
		sc, err := readCurrentRunSidecar(root)
		if err != nil || sc == nil || sc.IssueNumber != want {
			t.Errorf("readCurrentRunSidecar(%s) = %+v, %v; want issue %d", root, sc, err, want)
		}
	}

	// So is the run-state record the run lifecycle keeps.
	lcMain, _ := beginRunLifecycle(mainRoot, 11, "")
	lcLinked, _ := beginRunLifecycle(linked, 12, "")
	if lcMain.baseDir == "" || lcMain.baseDir == lcLinked.baseDir {
		t.Fatalf("run-state base dirs: main %q, linked %q — want two distinct", lcMain.baseDir, lcLinked.baseDir)
	}
	for dir, want := range map[string]int{lcMain.baseDir: 11, lcLinked.baseDir: 12} {
		rs, err := runstate.Load(dir)
		if err != nil || rs == nil || rs.IssueNumber != want {
			t.Errorf("run-state in %s = %+v, %v; want issue %d", dir, rs, err, want)
		}
	}
}

// Outside a git checkout there is no CHECKOUT: reads are absent and writes
// fail, never landing in the working tree.
func TestRunControlOutsideGitIsRefused(t *testing.T) {
	root := t.TempDir()
	if p := checkoutStatePath(root, layout.CheckoutCurrentRun); p != "" {
		t.Errorf("checkoutStatePath outside git = %q, want \"\"", p)
	}
	if err := writeCurrentRunSidecar(root, CurrentRunSidecar{IssueNumber: 1}); err == nil {
		t.Error("writeCurrentRunSidecar outside git succeeded")
	}
	if _, err := AutonomousStatePath(root); err == nil {
		t.Error("AutonomousStatePath outside git succeeded")
	}
}
