package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ---------------------------------------------------------------------------
// #398 — the OPEN-PR reconcile arm applies an IDENTITY ownership test.
//
// #3873 accepted an OPEN PR as proof that a non-terminal stage's failure was a
// phantom. Before #299 that arm was inert on worktree-isolated runs (the branch
// lookup answered "" so the probe never ran); #299 armed it on every run, which
// made a shape the #3873 rationale never contemplated reachable: conflict
// recovery rewinds pr-merge → feature-dev (#4072), THIS run's PR is open, and a
// genuine post-rewind feature-dev failure reconciles to a recorded success — on
// the one path where the run demonstrably has unfinished work.
//
// The fix is scoped, not total: a foreign OPEN PR (a prior run's, still in
// review) reconciles exactly as #3873 intended; this run's own OPEN PR requires
// MERGED; a PR that cannot be identified at all fails closed as own-run.
//
// "Own" is decided by IDENTITY — the PR number this run recorded at pr-create,
// or the fact that it reached pr-create at all — never by comparing commits.
// Both content tests were measured and both misclassify, in opposite directions:
//
//   - head-SHA equality calls an OWN PR foreign, because the rewind re-dispatch
//     rebases/commits on the branch and WIP checkpoints commit locally with the
//     push deferred, so the local tip outruns the pushed head;
//   - head-SHA equality calls a FOREIGN PR own, because issue-pickup reuses and
//     resets the branch to the pushed tip, so a re-run's checkout sits exactly at
//     a prior run's PR head.
//
// Both directions are pinned below, at the decision level and end-to-end.
// ---------------------------------------------------------------------------

// ownRunPRRunner drives a worktree-isolated run far enough to fail at an
// arbitrary non-terminal stage. Unlike worktreeIsolatedRunner (#299), which can
// only fail at the first stage because it writes no downstream contexts, this
// one writes EVERY successful stage's output context — resolved through the
// production stageOutputContextType map so the fixture cannot drift from the
// #2870 check and the next stage's prerequisite check it has to satisfy. That is
// what makes the post-#4072 rewind shape reachable here: the failure under test
// is a feature-dev failure that happens AFTER planning already succeeded.
type ownRunPRRunner struct {
	t         *testing.T
	worktree  string
	repo      string
	branch    string
	failStage state.PipelineStage

	mu    sync.Mutex
	calls map[state.PipelineStage]int
}

func newOwnRunPRRunner(t *testing.T, worktree, repo, branch string, failStage state.PipelineStage) *ownRunPRRunner {
	return &ownRunPRRunner{
		t:         t,
		worktree:  worktree,
		repo:      repo,
		branch:    branch,
		failStage: failStage,
		calls:     make(map[state.PipelineStage]int),
	}
}

func (r *ownRunPRRunner) count(stage state.PipelineStage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stage]
}

func (r *ownRunPRRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls[params.Stage]++
	r.mu.Unlock()

	// Production parity: the execution manager stamps the worktree onto the
	// runtime when it launches the stage process (internal/execution/manager.go).
	if params.Runtime != nil {
		params.Runtime.SetProcess(0, r.worktree)
	}

	if params.Stage == r.failStage {
		// Pre-flight death shape: non-zero exit, no stage error text, no output.
		return &StageRunResult{ExitCode: 1}, nil
	}

	if ctxType, ok := stageOutputContextType[params.Stage]; ok {
		sc := &stagecontext.StageContext{
			IssueNumber: params.IssueNumber,
			Repo:        r.repo,
			Branch:      r.branch,
			Stage:       string(params.Stage),
		}
		if err := stagecontext.Validate(sc); err != nil {
			r.t.Fatalf("fixture stage context for %s is invalid: %v", params.Stage, err)
		}
		path := stagecontext.ContextPath(r.worktree, params.IssueNumber, ctxType)
		if err := stagecontext.WriteContext(path, sc); err != nil {
			r.t.Fatalf("write %s context into worktree: %v", ctxType, err)
		}
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// writeRecordedPRContext writes the pr-{N}.json this run's pr-create records, in
// the shape skills/nightgauge-pr-create/SKILL.md Phase 4 specifies — the same
// document loadPRNumberForRecovery reads. It is the run's identity claim on a
// PR: "the PR I opened is #num".
func writeRecordedPRContext(t *testing.T, workspace string, issueNumber, num int) {
	t.Helper()
	dir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline dir: %v", err)
	}
	payload := map[string]any{
		"schema_version": "1.0",
		"issue_number":   issueNumber,
		"pr_number":      num,
		"pr_url":         fmt.Sprintf("https://github.com/nightgauge/nightgauge/pull/%d", num),
		"status":         "open",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal pr context: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("pr-%d.json", issueNumber))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write pr context: %v", err)
	}
}

// assertLogLineStartsWith asserts that some line of the captured log BEGINS with
// want (after the stdlib date/time prefix). Anchoring at line start matters:
// every scheduler line is `#<issue>: `-prefixed, so a bare strings.Contains can
// be satisfied by the wanted text appearing mid-line in unrelated output and
// pins nothing.
func assertLogLineStartsWith(t *testing.T, out, want, why string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[idx:]
		}
		if strings.HasPrefix(line, want) {
			return
		}
	}
	t.Errorf("%s.\nwant a log line starting with: %q\nlog was:\n%s", why, want, out)
}

// ---------------------------------------------------------------------------
// Decision-level: which PRs count as proof
// ---------------------------------------------------------------------------

// TestReconcileIssueResolved_OwnRunPrOpen_DoesNotReconcile is the #398 defect at
// the decision level: the OPEN PR the probe finds carries the number this run's
// pr-create recorded, so the run demonstrably has unfinished work and an OPEN PR
// is not proof the failure is a phantom.
//
// The head-SHA test this replaces answered "foreign" here — the rewind
// re-dispatch had already committed on the branch, so the local tip no longer
// matched the PR's pushed head — and reconciled the genuine failure away. Number
// identity is immune to that: commits move, PR numbers do not.
func TestReconcileIssueResolved_OwnRunPrOpen_DoesNotReconcile(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", testRecordedPRNumber)), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
	if got.reconciled() {
		t.Fatalf("an OPEN PR carrying the number this run recorded at pr-create must NOT reconcile: the run's work is "+
			"demonstrably unfinished, so the failure is real (#398); got %v", got)
	}
}

// TestReconcileIssueResolved_ReachedPRCreate_NoRecordedNumber_DoesNotReconcile
// pins the belt on the identity test. A run whose pr-create ran but whose
// pr-{N}.json was never written (or could not be read) records no number — yet a
// non-terminal stage can only be failing after pr-create because something
// rewound the run, which is precisely the shape where the branch's open PR is
// this run's. The completed-stage record answers it when the sidecar cannot.
func TestReconcileIssueResolved_ReachedPRCreate_NoRecordedNumber_DoesNotReconcile(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", testForeignPRNumber)), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", 0, true)
	if got.reconciled() {
		t.Fatalf("a run that has been to pr-create owns the branch's OPEN PR even with no recorded number — the "+
			"record-write-failed edge must not launder a real failure (#398); got %v", got)
	}
}

// TestReconcileIssueResolved_PRNumberMismatch covers both sides of the number
// comparison, which is the whole ownership test.
func TestReconcileIssueResolved_PRNumberMismatch(t *testing.T) {
	t.Run("a different number is a different run's PR", func(t *testing.T) {
		stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
			if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
				return []byte(`{"state":"OPEN"}`), nil
			}
			return prListPayload(pr("OPEN", testForeignPRNumber)), nil
		})

		got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
		if got != reconcilePrOpenStale {
			t.Fatalf("this run recorded PR #%d and the only OPEN PR is #%d — a prior run's, still in review, which is "+
				"#3873's case; got %v", testRecordedPRNumber, testForeignPRNumber, got)
		}
	})

	t.Run("the run's own PR present alongside a foreign one blocks", func(t *testing.T) {
		stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
			if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
				return []byte(`{"state":"OPEN"}`), nil
			}
			return prListPayload(
				pr("OPEN", testForeignPRNumber),
				pr("OPEN", testRecordedPRNumber),
			), nil
		})

		got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
		if got.reconciled() {
			t.Fatalf("the run's own OPEN PR is present, so its work is unfinished — a foreign PR alongside it does not "+
				"change that; got %v", got)
		}
	})
}

// TestReconcileIssueResolved_MergedAmongOwnOpen_ReconcilesViaMerged pins that
// pass 1 wins: a branch can carry both a merged PR and a later open one (the
// re-work shape), and the run's own OPEN PR must not veto the MERGED evidence.
// A single-pass "first interesting PR decides" scan would answer "blocked" here
// purely because of list order.
func TestReconcileIssueResolved_MergedAmongOwnOpen_ReconcilesViaMerged(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		// Own OPEN PR listed FIRST, merged PR second — the order that breaks a
		// single-pass scan.
		return prListPayload(
			pr("OPEN", testRecordedPRNumber),
			pr("MERGED", testForeignPRNumber),
		), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
	if got != reconcilePrMerged {
		t.Fatalf("a MERGED PR is unconditional proof the work landed and must win over the run's own OPEN PR; got %v", got)
	}
}

// TestReconcileIssueResolved_ForeignOpenBeforeOwnOpen_StillBlocked is the other
// half of the two-pass property. When a foreign OPEN PR is listed BEFORE this
// run's own OPEN PR, the foreign one must not decide the question: the run still
// has unfinished work. A single-pass scan reconciles here on list order alone —
// which is a real forge shape, since `gh pr list` orders by PR number and a
// prior run's PR is older.
func TestReconcileIssueResolved_ForeignOpenBeforeOwnOpen_StillBlocked(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(
			pr("OPEN", testForeignPRNumber),
			pr("OPEN", testRecordedPRNumber),
		), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
	if got.reconciled() {
		t.Fatalf("an own-run OPEN PR must block the reconcile even when a foreign OPEN PR is listed first; got %v", got)
	}
}

// TestReconcileIssueResolved_UnidentifiablePR_FailsClosed covers the rows where
// the identity comparison cannot be made at all, because the probe reported no
// usable PR number. This arm's mistake direction is laundering a real failure
// into a recorded success, so "could not tell" must read as "ours" — never as "a
// prior run's".
func TestReconcileIssueResolved_UnidentifiablePR_FailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		why     string
	}{
		{
			name:    "number field absent",
			payload: []byte(`[{"state":"OPEN"}]`),
			why:     "the probe's number field was missing from the entry",
		},
		{
			name:    "number reported as zero",
			payload: []byte(`[{"state":"OPEN","number":0}]`),
			why:     "the probe reported PR number 0, which identifies nothing",
		},
		{
			name:    "number reported as negative",
			payload: []byte(`[{"state":"OPEN","number":-1}]`),
			why:     "a negative PR number cannot be any run's PR",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"OPEN"}`), nil
				}
				return tc.payload, nil
			})

			// Recorded number present and pr-create not reached: nothing else in
			// the decision can be blamed for the block.
			got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)
			if got.reconciled() {
				t.Fatalf("an unidentifiable OPEN PR must fail closed (%s); got %v", tc.why, got)
			}
		})
	}
}

// TestBranchPrProbe_RequestsPRNumber guards the probe's field set. If `number`
// is ever dropped from `--json`, every PR reads as number 0, the fail-closed
// rule classifies every OPEN PR as own-run, and #3873's foreign-stale arm dies
// silently — no error, no log, just an arm that never fires again.
//
// It also guards the reverse: headRefOid is NOT requested. The head SHA answered
// this question wrongly in both directions and is gone; re-adding it to the
// query is the first step of re-adding the comparison.
func TestBranchPrProbe_RequestsPRNumber(t *testing.T) {
	var jsonFields string
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		for i, a := range args {
			if a == "--json" && i+1 < len(args) {
				jsonFields = args[i+1]
			}
		}
		return prListPayload(), nil
	})

	reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRecordedPRNumber, false)

	for _, field := range []string{"state", "number"} {
		if !strings.Contains(jsonFields, field) {
			t.Errorf("gh pr list --json = %q, must request %q — without it the ownership test cannot be made "+
				"and the foreign-stale-PR arm silently stops firing (#398)", jsonFields, field)
		}
	}
	if strings.Contains(jsonFields, "headRefOid") {
		t.Errorf("gh pr list --json = %q still requests headRefOid: ownership is identity, not content — a head SHA "+
			"calls an own PR foreign after a rewind commit and a foreign PR own after branch reuse (#398)", jsonFields)
	}
}

// TestHasReachedPRCreate pins the belt's source of truth: the run's own
// completed-stage record, read through the mutex-guarded snapshot.
func TestHasReachedPRCreate(t *testing.T) {
	if hasReachedPRCreate(nil) {
		t.Errorf("a nil runtime cannot have reached pr-create")
	}

	rs := state.NewRuntimeState("nightgauge/nightgauge", 398, "item-398")
	rs.BeginStage(state.StageFeatureDev)
	rs.CompleteStageWithCost(0, 10, 10, 0, 0)
	if hasReachedPRCreate(rs) {
		t.Errorf("a run that has only completed feature-dev has not been to pr-create")
	}

	rs.BeginStage(state.StagePRCreate)
	rs.CompleteStageWithCost(0, 10, 10, 0, 0)
	rs.BeginStage(state.StageFeatureDev)
	if !hasReachedPRCreate(rs) {
		t.Errorf("a run rewound to feature-dev AFTER pr-create must still report that it reached pr-create — that " +
			"rewind is the only way its own PR can be open while a feature stage fails (#398)")
	}
}

// TestReconcileOutcome_EnumHygiene pins that the accessors never conflate an
// unknown arm with reconcileNone. "No reconcile happened" and "an arm nobody
// taught these accessors about fired" are opposite claims, and a bare `default:`
// returning the reconcileNone text would report the second as the first.
func TestReconcileOutcome_EnumHygiene(t *testing.T) {
	unknown := reconcileOutcome(99)

	if got := unknown.String(); got == reconcileNone.String() {
		t.Errorf("unknown arm String() = %q, the same as reconcileNone — telemetry would record a fired arm as "+
			"\"no reconcile\"", got)
	}
	if got := unknown.String(); !strings.Contains(got, "99") {
		t.Errorf("unknown arm String() = %q, must name the unrecognized value so it is traceable", got)
	}
	if got := unknown.evidence(); got == reconcileNone.evidence() {
		t.Errorf("unknown arm evidence() = %q, the same as reconcileNone", got)
	}
	if got := unknown.completionReason(); got == reconcileNone.completionReason() {
		t.Errorf("unknown arm completionReason() = %q, the same as reconcileNone", got)
	}
	// The board write must stay conservative for an arm nobody mapped: it may
	// never claim a closure that was not observed.
	if got := completionBoardStatus(unknown); got != state.StatusInReview {
		t.Errorf("completionBoardStatus(unknown arm) = %q, want %q — an unmapped arm must never write Done",
			got, state.StatusInReview)
	}
}

// TestCompletionBoardStatus_PerArm pins the per-arm terminal board status
// mapping (#398). Done means exactly one thing in this codebase — the issue is
// CLOSED — so only the arm that observed that may write it.
func TestCompletionBoardStatus_PerArm(t *testing.T) {
	cases := []struct {
		arm  reconcileOutcome
		want state.BoardStatus
		why  string
	}{
		{reconcileNone, state.StatusInReview, "normal completion — unchanged from pre-#398 behavior"},
		{reconcileIssueClosed, state.StatusDone, "the issue is already closed; nothing left to review"},
		{
			reconcilePrMerged, state.StatusInReview,
			"the merged arm fires only after issueClosedOnForge answered NOT-closed, and since #299 the reconciled " +
				"run ends there, so nothing closes the issue later: Done would be a durable Done-with-open-issue",
		},
		{reconcilePrOpenStale, state.StatusInReview, "the work is in review, NOT merged — Done would be a lie"},
	}
	for _, tc := range cases {
		if got := completionBoardStatus(tc.arm); got != tc.want {
			t.Errorf("completionBoardStatus(%v) = %q, want %q — %s", tc.arm, got, tc.want, tc.why)
		}
	}
}

// TestReconcileIssueResolved_DeclineIsAlwaysLoud pins accepted advisory A: every
// exit that does NOT reconcile says why, naming the issue. The reconcile is the
// only thing between a real failure and a recorded success, so an operator
// asking "why did my run stay failed" must read the answer rather than infer it
// from silence.
func TestReconcileIssueResolved_DeclineIsAlwaysLoud(t *testing.T) {
	cases := []struct {
		name   string
		branch string
		gh     func(ctx context.Context, args ...string) ([]byte, error)
		want   string
	}{
		{
			name:   "own-run OPEN PR",
			branch: "fix/398-own-pr",
			gh: func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"OPEN"}`), nil
				}
				return prListPayload(pr("OPEN", testRecordedPRNumber)), nil
			},
			want: fmt.Sprintf("#398: non-terminal reconcile declined — OPEN PR #%d on nightgauge/acmeapp-platform "+
				"(branch fix/398-own-pr): PR #%d is this run's own (recorded at pr-create)",
				testRecordedPRNumber, testRecordedPRNumber),
		},
		{
			name:   "no MERGED and no OPEN PR",
			branch: "fix/398-own-pr",
			gh: func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"OPEN"}`), nil
				}
				return prListPayload(pr("CLOSED", testForeignPRNumber)), nil
			},
			want: "#398: non-terminal reconcile declined — nightgauge/acmeapp-platform (branch fix/398-own-pr) has " +
				"no MERGED PR and no OPEN PR among the 1 the probe listed",
		},
		{
			name:   "no branch to probe",
			branch: "",
			gh: func(_ context.Context, args ...string) ([]byte, error) {
				return []byte(`{"state":"OPEN"}`), nil
			},
			want: "#398: non-terminal reconcile declined — the issue is OPEN on nightgauge/acmeapp-platform and no " +
				"branch could be named",
		},
		{
			name:   "the probe itself failed",
			branch: "fix/398-own-pr",
			gh: func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"OPEN"}`), nil
				}
				return nil, fmt.Errorf("gh: API rate limit exceeded")
			},
			want: "#398: non-terminal reconcile declined — the branch-PR probe for fix/398-own-pr on " +
				"nightgauge/acmeapp-platform failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubReconcileGh(t, tc.gh)
			var got reconcileOutcome
			out := captureLog(t, func() {
				got = reconcileIssueResolved(context.Background(), acmeappItem(398), tc.branch, testRecordedPRNumber, false)
			})
			if got.reconciled() {
				t.Fatalf("fixture reconciled (%v) — it is supposed to exercise a DECLINE path", got)
			}
			assertLogLineStartsWith(t, out, tc.want,
				"a non-reconciling exit was silent, so an operator cannot tell why the run stayed failed")
		})
	}
}

// ---------------------------------------------------------------------------
// Scheduler level: the durable artifacts (history record, board status)
// ---------------------------------------------------------------------------

// TestScheduler_NonTerminalReconcile_OwnRunPrOpen_FailurePreserved is the #398
// defect end-to-end, on the shape that made it reachable: a worktree-isolated
// run (#299 armed the probe on every one of them) whose own PR is OPEN and whose
// feature-dev genuinely failed. The assertion is the durable artifact every
// consumer reads — the history record — because turning a real failure into a
// recorded success is the defect, and a reconciled run now ENDS at the
// reconciled stage (#299), so nothing downstream re-catches it.
//
// The fixture is the production-realistic rewind shape, not a contrived one: the
// run recorded PR #11 at pr-create, conflict recovery rewound it to feature-dev,
// and the re-dispatch committed on the branch with the push deferred — so the
// local tip is AHEAD of the PR's pushed head. That is exactly where a head-SHA
// ownership test read "foreign" and laundered the failure.
func TestScheduler_NonTerminalReconcile_OwnRunPrOpen_FailurePreserved(t *testing.T) {
	const (
		issueNumber   = 398
		repo          = "nightgauge/nightgauge"
		branch        = "fix/398-own-pr-ownership-test"
		recordedPRNum = 11
	)
	f := newWorktreeRunFixture(t, issueNumber, branch)
	// The PR points at the PUSHED tip; the rewind re-dispatch then commits on the
	// branch with the push deferred, so the local tip moves past it.
	gitx(t, f.worktree, "commit", "--allow-empty", "-m", "wip: rewind re-dispatch checkpoint")
	writeRecordedPRContext(t, f.worktree, issueNumber, recordedPRNum)

	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", recordedPRNum)), nil
	})

	runner := newOwnRunPRRunner(t, f.worktree, repo, branch, state.StageFeatureDev)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-398"}
	out := captureLog(t, func() {
		s.runPipeline(context.Background(), item)
	})

	if got := runner.count(state.StageFeatureDev); got != 1 {
		t.Fatalf("feature-dev ran %d times, want 1 — the fixture never reached the failing stage", got)
	}

	rec := findHistoryRecord(t, f.root, issueNumber)
	if rec.Outcome == "complete" {
		t.Errorf("run recorded outcome=%q: a genuine feature-dev failure was reconciled away because the run's OWN "+
			"PR was OPEN, so a real failure is on record as a success and nothing downstream re-catches it (#398)",
			rec.Outcome)
	}

	// The block must be legible in the log, on the issue it applies to.
	assertLogLineStartsWith(t, out,
		fmt.Sprintf("#%d: non-terminal reconcile declined — OPEN PR #%d on %s (branch %s): PR #%d is this run's own "+
			"(recorded at pr-create)", issueNumber, recordedPRNum, repo, branch, recordedPRNum),
		"the reconcile blocked a real failure from being laundered but did not say so")
}

// TestScheduler_NonTerminalReconcile_ForeignPrOpen_StillReconciles is the
// counterweight, and #398's second proven misclassification direction. Same run,
// same failure — but the OPEN PR is a prior run's, so the work IS in review and
// the phantom failure must still be reconciled away (#3873 preserved).
//
// The fixture is the branch-reuse shape production actually produces:
// issue-pickup reuses and RESETS the branch to the pushed tip ("reused-remote"),
// so this run's checkout sits exactly AT the prior run's PR head. Under the
// head-SHA test that equality read "ours" and blocked the reconcile — killing
// #3873's arm in the one shape it was written for. Identity is unmoved: this run
// recorded no PR and never reached pr-create, so no PR on the branch is its own.
func TestScheduler_NonTerminalReconcile_ForeignPrOpen_StillReconciles(t *testing.T) {
	const (
		issueNumber = 396
		repo        = "nightgauge/nightgauge"
		branch      = "fix/396-foreign-open-pr"
	)
	f := newWorktreeRunFixture(t, issueNumber, branch)

	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", testForeignPRNumber)), nil
	})

	runner := newOwnRunPRRunner(t, f.worktree, repo, branch, state.StageFeatureDev)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-396"}
	out := captureLog(t, func() {
		s.runPipeline(context.Background(), item)
	})

	// The premise: the checkout really is sitting at the head the prior run
	// pushed. If the fixture ever stops reusing the branch this test no longer
	// exercises the direction it claims to.
	if rootTip, wtTip := gitx(t, f.root, "rev-parse", "refs/heads/"+branch), gitx(t, f.worktree, "rev-parse", "HEAD"); rootTip != wtTip {
		t.Fatalf("fixture premise gone: worktree HEAD %s is not the reused branch tip %s", wtTip, rootTip)
	}

	rec := findHistoryRecord(t, f.root, issueNumber)
	if rec.Outcome != "complete" {
		t.Errorf("run recorded outcome=%q, want %q — #398 narrowed the OPEN-PR arm to this run's own PRs; a stale "+
			"prior-run PR must still reconcile (#3873 preserved)", rec.Outcome, "complete")
	}

	// A stale OPEN PR means the work is in review, NOT merged — Done would
	// misreport it.
	assertLogLineStartsWith(t, out,
		"#396: pipeline complete — resolved terminal board status In Review (arm pr_open_stale",
		"a run reconciled by a stale foreign OPEN PR must finish In Review, and the line must name the arm that decided it")
}

// TestScheduler_NonTerminalReconcile_PerArmBoardStatus pins the per-arm terminal
// board status through the full run (#398). The arm is decided deep inside the
// stage loop and consumed after it exits, so this is the test that the carrier
// actually survives the loop break — a per-iteration flag would pass every
// decision-level test above and still write In Review here.
func TestScheduler_NonTerminalReconcile_PerArmBoardStatus(t *testing.T) {
	cases := []struct {
		name       string
		issue      int
		branch     string
		issueState string
		prs        []byte // nil = no PRs on the branch
		wantStatus string
		wantArm    string
		why        string
	}{
		{
			name:       "issue closed on the forge",
			issue:      394,
			branch:     "fix/394-issue-closed",
			issueState: "CLOSED",
			wantStatus: "Done",
			wantArm:    "issue_closed",
			why:        "the issue is closed — the work shipped and there is nothing left to review",
		},
		{
			name:       "branch PR merged",
			issue:      393,
			branch:     "fix/393-pr-merged",
			issueState: "OPEN",
			prs:        prListPayload(pr("MERGED", testForeignPRNumber)),
			wantStatus: "In Review",
			wantArm:    "pr_merged",
			why: "the merged arm runs only after the issue answered NOT-closed and nothing closes it afterwards, " +
				"so Done would durably record Done-with-an-open-issue",
		},
		{
			name:       "stale foreign OPEN PR",
			issue:      392,
			branch:     "fix/392-foreign-open",
			issueState: "OPEN",
			prs:        prListPayload(pr("OPEN", testForeignPRNumber)),
			wantStatus: "In Review",
			wantArm:    "pr_open_stale",
			why:        "the work is in review, not merged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const repo = "nightgauge/nightgauge"
			f := newWorktreeRunFixture(t, tc.issue, tc.branch)

			prs := tc.prs
			if prs == nil {
				prs = prListPayload()
			}
			stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"` + tc.issueState + `"}`), nil
				}
				return prs, nil
			})

			runner := newOwnRunPRRunner(t, f.worktree, repo, tc.branch, state.StageFeatureDev)
			s := newWorktreeRunScheduler(f.root, runner)

			item := types.BoardItem{Number: tc.issue, Repo: repo, ID: "item-" + tc.branch}
			out := captureLog(t, func() {
				s.runPipeline(context.Background(), item)
			})

			want := "#" + strconv.Itoa(tc.issue) + ": pipeline complete — resolved terminal board status " +
				tc.wantStatus + " (arm " + tc.wantArm
			assertLogLineStartsWith(t, out, want, "wrong terminal board status — "+tc.why)
		})
	}
}
