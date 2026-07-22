package orchestrator

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// newAutonomousForCascadeTest builds a minimal AutonomousScheduler with the
// cascade tracker attached and `state` pre-populated with one running issue
// per call. We don't construct via NewAutonomousScheduler because that
// requires a full SchedulerConfig, github client, etc — orthogonal to what
// the cascade pause path actually depends on.
func newAutonomousForCascadeTest(t *testing.T, threshold int, window time.Duration) *AutonomousScheduler {
	t.Helper()
	// Clear env so the tracker reflects the explicit cfg we pass, not
	// whatever a developer might have set locally.
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	return &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:  "running",
			Running: nil,
		},
		rescanCh: make(chan struct{}, 4),
		cascadeTracker: NewCascadeTracker(CascadeTrackerConfig{
			Threshold: threshold,
			Window:    window,
		}),
	}
}

// addRunning is a thin helper so each test case can set up its own
// "running" entry before invoking onPipelineComplete.
func addRunning(as *AutonomousScheduler, repo string, number int, title string) {
	as.state.Running = append(as.state.Running, RunningItem{
		Repo:   repo,
		Number: number,
		Title:  title,
	})
}

// TestAutonomous_CascadePausesAfterThreshold is the end-to-end integration:
// drive the scheduler through 3 failures inside the window and assert that
// state.Status transitions to safety_tripped with the canonical pause tag.
func TestAutonomous_CascadePausesAfterThreshold(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)

	for i, num := range []int{100, 101, 102} {
		addRunning(as, "nightgauge/nightgauge", num, "issue")
		as.onPipelineComplete("nightgauge/nightgauge", num, false, false, "subagent_crash", "stage failed")
		if i < 2 {
			if as.state.Status == "safety_tripped" {
				t.Fatalf("scheduler tripped early on failure %d/3", i+1)
			}
		}
	}

	if as.state.Status != "safety_tripped" {
		t.Fatalf("Status = %q after 3 failures, want safety_tripped", as.state.Status)
	}
	if as.state.PauseTriggeredBy != CascadePauseReason {
		t.Errorf("PauseTriggeredBy = %q, want %q", as.state.PauseTriggeredBy, CascadePauseReason)
	}
	if !strings.Contains(as.state.PauseReason, "cascading-failures") {
		t.Errorf("PauseReason missing 'cascading-failures' tag: %q", as.state.PauseReason)
	}
	if !strings.Contains(as.state.PauseReason, "Manual triage required") {
		t.Errorf("PauseReason missing operator nudge: %q", as.state.PauseReason)
	}
}

// TestAutonomous_CascadeIgnoresStallKills asserts that recoverable terminal
// kinds (stall_kill, quota_exhausted, worktree_uncommitted, budget_ceiling)
// don't feed the cascade breaker — they short-circuit earlier in
// onPipelineComplete. Without this carve-out, a quiet half-hour of
// legitimate retries would burn down the threshold.
func TestAutonomous_CascadeIgnoresStallKills(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	for _, num := range []int{200, 201, 202, 203, 204} {
		addRunning(as, "nightgauge/nightgauge", num, "issue")
		as.onPipelineComplete("nightgauge/nightgauge", num, false, false, TerminalKindStallKill, "")
	}
	if as.state.Status == "safety_tripped" {
		t.Fatalf("scheduler tripped on stall_kill cluster; expected stall_kill to be excluded from cascade")
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on stall_kill cluster")
	}
}

// TestAutonomous_CascadeIgnoresQuotaExhausted mirrors the stall-kill test
// for the environmental quota exhaustion path.
func TestAutonomous_CascadeIgnoresQuotaExhausted(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	for _, num := range []int{300, 301, 302, 303} {
		addRunning(as, "nightgauge/nightgauge", num, "issue")
		as.onPipelineComplete("nightgauge/nightgauge", num, false, false, TerminalKindRateLimitQuotaExhausted, "")
	}
	if as.state.Status == "safety_tripped" {
		t.Errorf("scheduler tripped on quota_exhausted cluster; expected exclusion")
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on quota_exhausted")
	}
}

// TestAutonomous_PrMergeUnmerged_Recoverable pins the Issue #3691 contract
// (revised 2026-07-11 — sideline the issue, not the factory): when pr-merge
// "completes" but the PR didn't actually merge, autonomous MUST:
//
//   - NOT increment LifetimeIssueFailures (the work product is shipped and
//     waiting — a PR exists; counting it against the per-issue cap let a red
//     required check trip the whole scheduler on bowlsheet #233).
//   - NOT feed the cascade-failure breaker (a stuck PR is a single issue
//     needing attention, not a sign the pipeline is broken).
//   - KEEP the scheduler running — one externally-blocked PR must not pause
//     every unrelated Ready issue. The issue itself is sidelined to
//     "In review" (moveIssueToInReview) so it is not re-dispatched into the
//     same blocker.
func TestAutonomous_PrMergeUnmerged_Recoverable(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	repo := "acme/platform"
	issue := 949
	addRunning(as, repo, issue, "workspace payload upsert")

	detail := "[pr-merge-unmerged:ci_failures] PR #961 has 1 failing CI check(s): Lint, Typecheck, Test, Build. PR: https://github.com/acme/platform/pull/961 | failing-checks: Lint, Typecheck, Test, Build | recoverable: no LifetimeIssueFailures increment; resume after the blocker is resolved."
	as.onPipelineComplete(repo, issue, false, false, TerminalKindPrMergeUnmerged, detail)

	key := repo + "#" + strconv.Itoa(issue)
	if as.state.LifetimeIssueFailures[key] != 0 {
		t.Errorf("LifetimeIssueFailures[%s] = %d, want 0 (recoverable kind must not increment lifetime cap)",
			key, as.state.LifetimeIssueFailures[key])
	}
	if as.state.Status != "running" {
		t.Errorf("state.Status = %q, want %q (an externally-blocked PR must sideline the issue, not pause the scheduler)",
			as.state.Status, "running")
	}
	if as.state.PauseTriggeredBy != "" {
		t.Errorf("PauseTriggeredBy = %q, want empty (no pause on pr-merge-unmerged)",
			as.state.PauseTriggeredBy)
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on pr-merge-unmerged; expected exclusion")
	}
	// The unstamped post-merge-verification phrasing must classify to the
	// same recoverable kind — pre-fix it fell through to the generic path
	// and burned the lifetime cap (bowlsheet #233/#244).
	if got := ClassifyTerminalKind(
		`pr-merge reported success but PR #276 is not merged (state: OPEN). blocked by failing check "Sync E2E (Docker)" (mergeStateStatus=BLOCKED). Pipeline halted after 2 verification attempts.`,
	); got != TerminalKindPrMergeUnmerged {
		t.Errorf("ClassifyTerminalKind(post-merge-verification phrasing) = %q, want %q",
			got, TerminalKindPrMergeUnmerged)
	}
}

// TestAutonomous_CascadeResetsOnResume verifies that an explicit operator
// Resume clears the breaker so the next cluster can re-trip. This is the
// "manual triage" contract from the issue text.
func TestAutonomous_CascadeResetsOnResume(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.safetyRails = NewSafetyRails(DefaultSafetyConfig()) // Resume() requires safetyRails too

	for _, num := range []int{400, 401, 402} {
		addRunning(as, "r", num, "issue")
		as.onPipelineComplete("r", num, false, false, "subagent_crash", "")
	}
	if !as.cascadeTracker.IsTripped() {
		t.Fatalf("setup: expected cascade trip")
	}

	as.Resume()
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker still tripped after Resume; want Reset")
	}
	if got := as.cascadeTracker.CountInWindow(time.Now()); got != 0 {
		t.Errorf("CountInWindow after Resume = %d, want 0", got)
	}
}

// TestAutonomous_CascadeFiresStatusChange ensures the cascade pause path
// emits an autonomous.statusChanged signal so the IPC server can light up
// the VSCode badge and the DiscordService can fire a webhook.
func TestAutonomous_CascadeFiresStatusChange(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	var observed []AutonomousStatusChange
	done := make(chan struct{}, 1)
	as.onStatusChange = func(snap AutonomousStatusChange) {
		observed = append(observed, snap)
		if snap.Status == "safety_tripped" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}

	for _, num := range []int{500, 501, 502} {
		addRunning(as, "r", num, "issue")
		as.onPipelineComplete("r", num, false, false, "subagent_crash", "")
	}

	select {
	case <-done:
		// ok — async callback fired
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for safety_tripped status change; observed=%+v", observed)
	}

	// Confirm the last observed status carries the canonical tag.
	last := observed[len(observed)-1]
	if last.PauseTriggeredBy != CascadePauseReason {
		t.Errorf("PauseTriggeredBy = %q, want %q", last.PauseTriggeredBy, CascadePauseReason)
	}
}

// TestAutonomous_CascadeOnlyFiresOnce asserts that a 4th, 5th failure after
// the breaker trips does NOT emit additional status-change events with the
// cascade tag — the Discord webhook should fire exactly once per trip.
func TestAutonomous_CascadeOnlyFiresOnce(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	cascadeStatusEvents := 0
	as.onStatusChange = func(snap AutonomousStatusChange) {
		if snap.PauseTriggeredBy == CascadePauseReason {
			cascadeStatusEvents++
		}
	}

	for _, num := range []int{600, 601, 602, 603, 604, 605} {
		addRunning(as, "r", num, "issue")
		as.onPipelineComplete("r", num, false, false, "subagent_crash", "")
	}

	// Give async callbacks time to land.
	time.Sleep(100 * time.Millisecond)
	if cascadeStatusEvents != 1 {
		t.Errorf("cascade status change fired %d times, want exactly 1", cascadeStatusEvents)
	}
}

// TestAutonomous_CascadeBreakerNilTrackerIsNoop guards the construction
// path: if anything ever drops the cascadeTracker initialization, the
// onPipelineComplete path must still work (just without cascade detection).
func TestAutonomous_CascadeBreakerNilTrackerIsNoop(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.cascadeTracker = nil // simulate older state file or test scaffold

	for _, num := range []int{700, 701, 702} {
		addRunning(as, "r", num, "issue")
		// Must not panic.
		as.onPipelineComplete("r", num, false, false, "subagent_crash", "")
	}
	if as.state.Status == "safety_tripped" {
		t.Errorf("nil tracker should not trip safety; got %q", as.state.Status)
	}
}
