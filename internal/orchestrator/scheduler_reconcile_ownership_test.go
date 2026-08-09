package orchestrator

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ---------------------------------------------------------------------------
// #398 — the OPEN-PR reconcile arm applies an ownership test.
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
// MERGED; unknowable ownership fails closed as own-run.
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

// ---------------------------------------------------------------------------
// Decision-level: which PRs count as proof
// ---------------------------------------------------------------------------

// TestReconcileIssueResolved_OwnRunPrOpen_DoesNotReconcile is the #398 defect at
// the decision level: the OPEN PR the probe finds is THIS run's own PR (its head
// SHA is the run's branch tip), so the run demonstrably has unfinished work and
// an OPEN PR is not proof the failure is a phantom. Pre-fix this returned
// "resolved" and the run's real failure was recorded as a success.
func TestReconcileIssueResolved_OwnRunPrOpen_DoesNotReconcile(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", testRunBranchTip, 11)), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRunBranchTip)
	if got.reconciled() {
		t.Fatalf("an OPEN PR whose head SHA is this run's own branch tip must NOT reconcile: the run's work is "+
			"demonstrably unfinished, so the failure is real (#398); got %v", got)
	}
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
			pr("OPEN", testRunBranchTip, 11),
			pr("MERGED", testForeignPRTip, 7),
		), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRunBranchTip)
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
			pr("OPEN", testForeignPRTip, 5),
			pr("OPEN", testRunBranchTip, 11),
		), nil
	})

	got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRunBranchTip)
	if got.reconciled() {
		t.Fatalf("an own-run OPEN PR must block the reconcile even when a foreign OPEN PR is listed first; got %v", got)
	}
}

// TestReconcileIssueResolved_UnknowableOwnership_FailsClosed covers the rows
// where the SHA comparison cannot be made at all. This arm's mistake direction
// is laundering a real failure into a recorded success, so "could not look" must
// read as "ours" — never as "a prior run's".
func TestReconcileIssueResolved_UnknowableOwnership_FailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		localTip string
		prHead   string
		why      string
	}{
		{
			name:     "local tip unresolved",
			localTip: "",
			prHead:   testForeignPRTip,
			why:      "the run's branch tip could not be read (detached worktree, deleted branch, git failure)",
		},
		{
			name:     "forge returned no head SHA",
			localTip: testRunBranchTip,
			prHead:   "",
			why:      "the probe's headRefOid field was absent or empty",
		},
		{
			name:     "neither side known",
			localTip: "",
			prHead:   "",
			why:      "nothing on either side identifies the PR",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
				if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
					return []byte(`{"state":"OPEN"}`), nil
				}
				return prListPayload(pr("OPEN", tc.prHead, 11)), nil
			})

			got := reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", tc.localTip)
			if got.reconciled() {
				t.Fatalf("unknowable PR ownership must fail closed (%s); got %v", tc.why, got)
			}
		})
	}
}

// TestBranchPrProbe_RequestsHeadRefOid guards the probe's field set. If
// headRefOid is ever dropped from `--json`, every PR reads with an empty head,
// the fail-closed rule classifies every OPEN PR as own-run, and #3873's
// foreign-stale arm dies silently — no error, no log, just an arm that never
// fires again.
func TestBranchPrProbe_RequestsHeadRefOid(t *testing.T) {
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

	reconcileIssueResolved(context.Background(), acmeappItem(398), "fix/398-own-pr", testRunBranchTip)

	for _, field := range []string{"state", "headRefOid"} {
		if !strings.Contains(jsonFields, field) {
			t.Errorf("gh pr list --json = %q, must request %q — without it the ownership test cannot be made "+
				"and the foreign-stale-PR arm silently stops firing (#398)", jsonFields, field)
		}
	}
}

// TestLocalBranchTip_ResolvesAndFailsSoft pins both halves of the tip lookup:
// it returns the real SHA for a branch that exists in the checkout the stages
// ran in, and it swallows every failure into "" rather than propagating an
// error — the fail-closed rule downstream is what turns "" into a decision.
func TestLocalBranchTip_ResolvesAndFailsSoft(t *testing.T) {
	f := newWorktreeRunFixture(t, 398, "fix/398-tip")
	want := gitx(t, f.root, "rev-parse", "refs/heads/fix/398-tip")

	if got := localBranchTip(f.worktree, "fix/398-tip"); got != want {
		t.Errorf("localBranchTip(worktree) = %q, want the branch tip %q", got, want)
	}
	if got := localBranchTip(f.worktree, "fix/398-does-not-exist"); got != "" {
		t.Errorf("a branch that does not exist must resolve to \"\", got %q", got)
	}
	if got := localBranchTip("", "fix/398-tip"); got != "" {
		t.Errorf("an empty workspace must resolve to \"\", got %q", got)
	}
	if got := localBranchTip(f.worktree, ""); got != "" {
		t.Errorf("an empty branch must resolve to \"\", got %q", got)
	}
}

// TestCompletionBoardStatus_PerArm pins the per-arm terminal board status
// mapping (#398, issue comment 2). Only evidence that the work SHIPPED promotes
// a run to Done; a reconcile backed by a stale open PR leaves work in review,
// and so does every normal completion.
func TestCompletionBoardStatus_PerArm(t *testing.T) {
	cases := []struct {
		arm  reconcileOutcome
		want state.BoardStatus
		why  string
	}{
		{reconcileNone, state.StatusInReview, "normal completion — unchanged from pre-#398 behavior"},
		{reconcileIssueClosed, state.StatusDone, "the issue is already closed; nothing left to review"},
		{reconcilePrMerged, state.StatusDone, "the PR already merged; nothing left to review"},
		{reconcilePrOpenStale, state.StatusInReview, "the work is in review, NOT merged — Done would be a lie"},
	}
	for _, tc := range cases {
		if got := completionBoardStatus(tc.arm); got != tc.want {
			t.Errorf("completionBoardStatus(%v) = %q, want %q — %s", tc.arm, got, tc.want, tc.why)
		}
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
func TestScheduler_NonTerminalReconcile_OwnRunPrOpen_FailurePreserved(t *testing.T) {
	const (
		issueNumber = 398
		repo        = "nightgauge/nightgauge"
		branch      = "fix/398-own-pr-ownership-test"
	)
	f := newWorktreeRunFixture(t, issueNumber, branch)
	// The real tip of the real branch the run executes on — the same value the
	// scheduler resolves through localBranchTip, not a hand-written constant.
	tip := gitx(t, f.root, "rev-parse", "refs/heads/"+branch)

	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("OPEN", tip, 11)), nil
	})

	runner := newOwnRunPRRunner(t, f.worktree, repo, branch, state.StageFeatureDev)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-398"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageFeatureDev); got != 1 {
		t.Fatalf("feature-dev ran %d times, want 1 — the fixture never reached the failing stage", got)
	}

	rec := findHistoryRecord(t, f.root, issueNumber)
	if rec.Outcome == "complete" {
		t.Errorf("run recorded outcome=%q: a genuine feature-dev failure was reconciled away because the run's OWN "+
			"PR was OPEN, so a real failure is on record as a success and nothing downstream re-catches it (#398)",
			rec.Outcome)
	}
}

// TestScheduler_NonTerminalReconcile_ForeignPrOpen_StillReconciles is the
// counterweight: the #398 narrowing must not delete #3873's arm. Same run, same
// failure — but the OPEN PR's head is a prior run's commit, so the work IS in
// review and the phantom failure must still be reconciled away.
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
		return prListPayload(pr("OPEN", testForeignPRTip, 5)), nil
	})

	runner := newOwnRunPRRunner(t, f.worktree, repo, branch, state.StageFeatureDev)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-396"}
	out := captureLog(t, func() {
		s.runPipeline(context.Background(), item)
	})

	rec := findHistoryRecord(t, f.root, issueNumber)
	if rec.Outcome != "complete" {
		t.Errorf("run recorded outcome=%q, want %q — #398 narrowed the OPEN-PR arm to this run's own PRs; a stale "+
			"prior-run PR must still reconcile (#3873 preserved)", rec.Outcome, "complete")
	}

	// A stale OPEN PR means the work is in review, NOT merged — Done would
	// misreport it.
	const wantStatus = "#396: pipeline complete — board status In Review"
	if !strings.Contains(out, wantStatus) {
		t.Errorf("a run reconciled by a stale foreign OPEN PR must finish In Review, not Done.\nwant substring: %q\nlog was:\n%s",
			wantStatus, out)
	}
}

// TestScheduler_NonTerminalReconcile_PerArmBoardStatus pins the per-arm terminal
// board status through the full run (#398, issue comment 2). The arm is decided
// deep inside the stage loop and consumed after it exits, so this is the test
// that the carrier actually survives the loop break — a per-iteration flag would
// pass every decision-level test above and still write In Review here.
func TestScheduler_NonTerminalReconcile_PerArmBoardStatus(t *testing.T) {
	cases := []struct {
		name       string
		issue      int
		branch     string
		issueState string
		prs        []byte // nil = use the merged-PR fixture
		wantStatus string
		why        string
	}{
		{
			name:       "issue closed on the forge",
			issue:      394,
			branch:     "fix/394-issue-closed",
			issueState: "CLOSED",
			wantStatus: "Done",
			why:        "the issue is closed — the work shipped and there is nothing left to review",
		},
		{
			name:       "branch PR merged",
			issue:      393,
			branch:     "fix/393-pr-merged",
			issueState: "OPEN",
			prs:        prListPayload(pr("MERGED", testForeignPRTip, 7)),
			wantStatus: "Done",
			why:        "the PR merged — the work shipped",
		},
		{
			name:       "stale foreign OPEN PR",
			issue:      392,
			branch:     "fix/392-foreign-open",
			issueState: "OPEN",
			prs:        prListPayload(pr("OPEN", testForeignPRTip, 5)),
			wantStatus: "In Review",
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

			want := "#" + strconv.Itoa(tc.issue) + ": pipeline complete — board status " + tc.wantStatus
			if !strings.Contains(out, want) {
				t.Errorf("wrong terminal board status — %s.\nwant substring: %q\nlog was:\n%s", tc.why, want, out)
			}
		})
	}
}
