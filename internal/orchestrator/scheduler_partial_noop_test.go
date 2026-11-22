package orchestrator

// pr-create's no-diff settlement of a partial-delivery re-run (#3487): booked
// where the run returns, never retried, and never outliving its attempt.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/internal/trace"
	"github.com/nightgauge/nightgauge/pkg/types"
)

const settlementIssue = 3487

// scriptedPRCreateRunner answers each pr-create attempt with attempt(n), from 1.
type scriptedPRCreateRunner struct {
	calls   int
	attempt func(n int) pmstages.PRCreateResult
}

func (r *scriptedPRCreateRunner) Run(context.Context, int, string, string) (pmstages.PRCreateResult, error) {
	r.calls++
	return r.attempt(r.calls), nil
}

func noDiffPunt() pmstages.PRCreateResult {
	return pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonNoDiffAgainstBase, Branch: "feat/3487", BaseBranch: "main"}
}

// failingPRCreateSkill is a pr-create whose skill, reached on a punt, fails.
type failingPRCreateSkill struct{ *partialRerunRunner }

func (r failingPRCreateSkill) RunStage(ctx context.Context, p StageRunParams) (*StageRunResult, error) {
	res, err := r.partialRerunRunner.RunStage(ctx, p)
	if p.Stage != state.StagePRCreate || err != nil {
		return res, err
	}
	return &StageRunResult{ExitCode: 1, ErrorText: "stage exited 1: the model produced no usable output"}, nil
}

// settlementRun reaches pr-create on a branch level with its base: the stop
// before pr-create needs an unticked box, and this body has none. Escalation
// is the default single one.
func settlementRun(t *testing.T, scoped bool, pr pmstages.PRCreateRunner) (*Scheduler, string, *partialRerunRunner) {
	t.Helper()
	runner := &partialRerunRunner{}
	s, root := partialRerunScheduler(t, failingPRCreateSkill{runner}, false)
	s.retryEngine = NewRetryEngine(DefaultRetryConfig())
	s.recordOutcomes = true
	s.partialDeliveryRead = func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
		return hooks.PartialDeliveryStatus{Criteria: []string{}}, nil
	}
	if scoped {
		s.partialDeliveryRead = pendingRemainder(3141, partialRerunCriterion)
		// feature-dev leaves a diff, so the run passes its own stop, and
		// feature-validate reverts it.
		var made string
		runner.onStage = func(p StageRunParams) {
			switch p.Stage {
			case state.StageFeatureDev:
				wt := runWorktreeFor(t, root, "issue-3487")
				if wt == "" {
					t.Error("feature-dev ran with no run worktree: the fixture cannot leave a diff")
					return
				}
				made = filepath.Join(wt, "change_test.go")
				if err := os.WriteFile(made, []byte("package x\n"), 0o644); err != nil {
					t.Error(err)
				}
			case state.StageFeatureValidate:
				if err := os.Remove(made); err != nil {
					t.Error(err)
				}
			}
		}
	}
	s.issueSvc.(*mockIssueSvc).addIssue("acme", "widget", settlementIssue, &types.Issue{
		NodeID: "I_3487", Number: settlementIssue, State: "OPEN", Body: "Criteria, as prose: " + partialRerunCriterion + "\n",
	})
	s.noDiffCommenter = &recordingCommenter{}
	s.prCreateRunner = pr
	return s, root, runner
}

// settledRun is what one run left for its consumers.
type settledRun struct {
	kind      string
	snap      *state.RuntimeState
	record    state.V2RunRecord
	prCreate  []diagnostics.StageExitRecord
	corpus    int
	completed bool
}

func runSettlement(t *testing.T, s *Scheduler, root string) settledRun {
	t.Helper()
	var out settledRun
	s.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) { out.snap = rt })
	out.completed, out.kind = s.runPipeline(context.Background(), types.BoardItem{
		Number: settlementIssue, Repo: "acme/widget", ID: "item-id", NodeID: "I_3487", Title: "no-op",
		Labels: []string{"type:feature"},
	})
	if out.snap == nil {
		t.Fatal("the run never completed")
	}
	for _, rec := range readExitRecords(t, root) {
		if rec.Stage == string(state.StagePRCreate) {
			out.prCreate = append(out.prCreate, rec)
		}
	}
	out.corpus = corpusRowCount(t, root)
	files, err := filepath.Glob(filepath.Join(layouttest.PipelineDir(t, root), "history", "20*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("history files = %v (%v)", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(splitNonEmptyLines(string(data))[0]), &out.record); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertBookedNoOp(t *testing.T, run settledRun) {
	t.Helper()
	if run.completed || run.kind != TerminalKindNotPipelineActionable || !run.snap.PartialNoOp {
		t.Fatalf("kind = %q flag = %v, want a booked no-op", run.kind, run.snap.PartialNoOp)
	}
	if run.record.Outcome != "cancelled" || run.record.OutcomeType != OutcomeTypeNoOp || run.corpus != 0 {
		t.Errorf("record = %s/%s corpus = %d, want cancelled/no_op and no row", run.record.Outcome, run.record.OutcomeType, run.corpus)
	}
	last := run.prCreate[len(run.prCreate)-1]
	if last.Success || last.GateKind != diagnostics.GateKindPartialNoOp || last.TerminalKind != TerminalKindNotPipelineActionable || !last.IsSkip() {
		t.Errorf("exit record = %+v, want success:false with partial_no_op", last)
	}
}

// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestPartialNoOpSettlement_IsNotRetried(t *testing.T) {
	pr := &scriptedPRCreateRunner{attempt: func(int) pmstages.PRCreateResult { return noDiffPunt() }}
	s, root, runner := settlementRun(t, true, pr)
	run := runSettlement(t, s, root)

	assertBookedNoOp(t, run)
	if pr.calls != 1 || len(run.snap.EscalationHistory) != 0 || ranStage(runner, state.StagePRCreate) {
		t.Errorf("attempts = %d escalations = %+v, want one attempt and no retry", pr.calls, run.snap.EscalationHistory)
	}
	if len(run.prCreate) != 1 {
		t.Fatalf("pr-create exit records = %d, want 1", len(run.prCreate))
	}
	// The stage-exit trace event carries the same structured kind.
	events, err := trace.ReadRun(root, run.snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var prExit []trace.StageExitPayload
	for _, ev := range events {
		if ev.Kind != trace.KindStageExit || ev.Stage != string(state.StagePRCreate) {
			continue
		}
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var p trace.StageExitPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		prExit = append(prExit, p)
	}
	if len(prExit) != 1 || prExit[0].Success || prExit[0].GateKind != diagnostics.GateKindPartialNoOp ||
		prExit[0].TerminalKind != TerminalKindNotPipelineActionable {
		t.Errorf("pr-create stage_exit payloads = %+v, want one with partial_no_op and not_pipeline_actionable", prExit)
	}
	// The issue stays open, so what it blocks is held (#3487).
	if !s.noOpBlockers[repoIssueKey("acme/widget", settlementIssue)] {
		t.Error("the settled no-op holds none of its successors")
	}
	// The outcome classifier's failure total leaves it out.
	failed := run.prCreate[0]
	failed.GateKind, failed.TerminalKind = "fail", TerminalKindSubagentCrash
	if stats := diagnostics.ComputeStats([]diagnostics.StageExitRecord{run.prCreate[0], failed}); len(stats) != 1 || stats[0].N != 1 {
		t.Errorf("stats = %+v, want only the failure counted", stats)
	}
}

// The first attempt settles a no-op, and the post-stage protected-path check
// then fails that attempt, so it is escalated. The retry is blocked by the
// security scan: the run is that failure, whatever the settlement said.
//
// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestPartialNoOpSettlement_DoesNotOutliveItsAttempt(t *testing.T) {
	var root, protected string
	pr := &scriptedPRCreateRunner{}
	pr.attempt = func(n int) pmstages.PRCreateResult {
		if n == 1 {
			ws := runWorktreeFor(t, root, "issue-3487")
			if ws == "" {
				t.Error("pr-create ran with no run worktree: the fixture cannot touch a protected path")
				return noDiffPunt()
			}
			protected = filepath.Join(ws, ".github", "publication-boundary.yaml")
			if err := os.MkdirAll(filepath.Dir(protected), 0o755); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(protected, []byte("allow: []\n"), 0o644); err != nil {
				t.Error(err)
			}
			return noDiffPunt()
		}
		if err := os.Remove(protected); err != nil {
			t.Error(err)
		}
		return pmstages.PRCreateResult{Path: pmstages.CreatePathBlocked, Reason: "security_scan", Detail: "a secret-shaped string in scan.go"}
	}
	var s *Scheduler
	s, root, _ = settlementRun(t, true, pr)
	run := runSettlement(t, s, root)

	if pr.calls != 2 || len(run.snap.EscalationHistory) != 1 {
		t.Fatalf("attempts = %d escalations = %+v; the fixture must settle, then retry", pr.calls, run.snap.EscalationHistory)
	}
	if detail := run.snap.StageErrors[string(state.StagePRCreate)]; !strings.Contains(detail, "security_scan") {
		t.Fatalf("stage error = %q, want the security-scan block", detail)
	}
	if run.snap.PartialNoOp || run.kind == TerminalKindNotPipelineActionable {
		t.Errorf("flag = %v kind = %q: the settlement outlived its attempt", run.snap.PartialNoOp, run.kind)
	}
	if run.record.Outcome != "failed" || run.record.OutcomeType == OutcomeTypeNoOp ||
		run.record.TerminalFailureKind == TerminalKindNotPipelineActionable || run.corpus != 1 {
		t.Errorf("record = %s/%s/%s corpus = %d, want that failure's", run.record.Outcome, run.record.OutcomeType, run.record.TerminalFailureKind, run.corpus)
	}
	if got := schedulerTerminalOutcome(false, run.kind, run.snap.PartialNoOp); got != "failed" {
		t.Errorf("terminal outcome = %q, want failed", got)
	}
	if last := run.prCreate[len(run.prCreate)-1]; last.IsSkip() || last.GateKind == diagnostics.GateKindPartialNoOp {
		t.Errorf("exit record = %+v, want the failure's", last)
	}
}

// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestPartialNoOpSettlement_EscalatedRetrySettles(t *testing.T) {
	pr := &scriptedPRCreateRunner{attempt: func(n int) pmstages.PRCreateResult {
		if n == 1 {
			return pmstages.PRCreateResult{Path: pmstages.CreatePathPunt, Reason: pmstages.ReasonUnexpected}
		}
		return noDiffPunt()
	}}
	s, root, _ := settlementRun(t, true, pr)
	run := runSettlement(t, s, root)

	if pr.calls != 2 || len(run.snap.EscalationHistory) != 1 {
		t.Fatalf("attempts = %d escalations = %+v, want a failure, then its retry", pr.calls, run.snap.EscalationHistory)
	}
	assertBookedNoOp(t, run)
	if run.prCreate[0].IsSkip() {
		t.Errorf("the first attempt's failure became a skip: %+v", run.prCreate[0])
	}
}

// Serial: swaps package var reconcileExecGh (via partialRerunScheduler).
func TestPartialNoOpSettlement_UnscopedStaysAFailure(t *testing.T) {
	pr := &scriptedPRCreateRunner{attempt: func(int) pmstages.PRCreateResult { return noDiffPunt() }}
	s, root, _ := settlementRun(t, false, pr)
	run := runSettlement(t, s, root)

	if pr.calls == 0 || run.snap.PartialNoOp {
		t.Fatalf("attempts = %d flag = %v", pr.calls, run.snap.PartialNoOp)
	}
	if len(s.noOpBlockers) != 0 {
		t.Errorf("an unscoped failure placed a no-op hold: %v", s.noOpBlockers)
	}
	if run.record.Outcome != "failed" || run.record.OutcomeType == OutcomeTypeNoOp {
		t.Errorf("record = %s/%s, want main's failure", run.record.Outcome, run.record.OutcomeType)
	}
	if last := run.prCreate[len(run.prCreate)-1]; last.IsSkip() || last.GateKind == diagnostics.GateKindPartialNoOp {
		t.Errorf("exit record = %+v, want main's failure", last)
	}
}

// Go's no-op comments are no record, whatever the criterion says (#3487).
func TestNoDiffComments_CarryNoPartialDeliveryMarker(t *testing.T) {
	t.Parallel()

	const marker = "<!-- nightgauge:partial-delivery pr=9 -->"
	crit := "handles the export " + marker
	_, settled := pmstages.NoDiffOutcome(7, "feat/7-x", "main", []pmstages.ACVerdict{{Text: crit, Status: pmstages.ACUnmet}})
	for name, body := range map[string]string{
		"settlement":       settled,
		"before pr-create": pmstages.NoDiffBeforePRCreateComment(7, "feat/7-x", "main", []string{crit}),
		"after dev":        pmstages.PartialRerunNoDiffComment(7, 5, "feat/7-x", "main", []string{crit}),
	} {
		if pr, _, ok := hooks.ParsePartialDeliveryComment(body); ok || strings.Contains(body, marker) {
			t.Errorf("%s: pr=%d, the criterion's marker survived:\n%s", name, pr, body)
		}
	}
}

// noOpHeldQueue is a scheduler whose queue holds acme/widget#11, blocked by
// #3487, after #3487's run ended a partial-delivery no-op.
func noOpHeldQueue(t *testing.T, issueSvc *mockIssueSvc) *Scheduler {
	t.Helper()
	s := &Scheduler{repoRunning: map[string]int{}, mergeLocks: map[string]*sync.Mutex{}, maxPerRepo: 4, trustGate: allWritersGate()}
	if issueSvc != nil {
		s.issueSvc = issueSvc
	}
	s.queue = []QueueItem{{Repo: "acme/widget", IssueNumber: 11, Status: "pending",
		BlockedBy: []QueueBlockingRef{{Number: settlementIssue, State: "OPEN"}}}}
	s.HoldNoOpSuccessors("acme/widget", settlementIssue)
	return s
}

func dequeuedNumbers(s *Scheduler) []int {
	var got []int
	for _, it := range s.DequeueIndependent(context.Background(), 4, nil) {
		got = append(got, it.IssueNumber)
	}
	return got
}

// The scheduler holds a no-op's successor while the no-op issue is open and
// releases it on the first poll after the issue closes, by the card's close
// or by hand, dropping the hold either way (#3487).
func TestNoOpBlocker_HoldsUntilClosed(t *testing.T) {
	t.Parallel()
	key := repoIssueKey("acme/widget", settlementIssue)

	t.Run("open blocker stays queued", func(t *testing.T) {
		t.Parallel()
		mock := newMockIssueSvc()
		mock.addIssue("acme", "widget", settlementIssue, &types.Issue{Number: settlementIssue, State: "OPEN"})
		s := noOpHeldQueue(t, mock)
		for poll := 1; poll <= 2; poll++ {
			if got := dequeuedNumbers(s); len(got) != 0 {
				t.Fatalf("poll %d dispatched %v while #%d is open", poll, got, settlementIssue)
			}
		}
		if !s.noOpBlockers[key] {
			t.Error("the hold was dropped while the blocker is open")
		}
	})

	t.Run("card close releases on the next poll", func(t *testing.T) {
		t.Parallel()
		// The forge still reads OPEN: the release must not wait for it.
		mock := newMockIssueSvc()
		mock.addIssue("acme", "widget", settlementIssue, &types.Issue{Number: settlementIssue, State: "OPEN"})
		s := noOpHeldQueue(t, mock)
		s.ReleaseNoOpBlocker("acme/widget", settlementIssue)
		if s.noOpBlockers[key] {
			t.Error("the hold outlived the card's close")
		}
		if got := s.queue[0].BlockedBy[0].State; got != "CLOSED" {
			t.Errorf("blocker state = %s, want CLOSED", got)
		}
		if got := dequeuedNumbers(s); fmt.Sprint(got) != "[11]" {
			t.Errorf("dequeued %v after the card closed #%d, want [11]", got, settlementIssue)
		}
	})

	t.Run("close by hand releases on the next poll", func(t *testing.T) {
		t.Parallel()
		mock := newMockIssueSvc()
		mock.addIssue("acme", "widget", settlementIssue, &types.Issue{Number: settlementIssue, State: "CLOSED"})
		s := noOpHeldQueue(t, mock)
		if got := dequeuedNumbers(s); fmt.Sprint(got) != "[11]" {
			t.Errorf("dequeued %v after #%d was closed by hand, want [11]", got, settlementIssue)
		}
		if s.noOpBlockers[key] {
			t.Error("the hold outlived a close by hand")
		}
	})

	t.Run("a dispatch of the no-op issue drops its hold", func(t *testing.T) {
		t.Parallel()
		s := noOpHeldQueue(t, nil)
		s.queue = []QueueItem{{Repo: "acme/widget", IssueNumber: settlementIssue, Status: "pending"}}
		if got := dequeuedNumbers(s); fmt.Sprint(got) != fmt.Sprint([]int{settlementIssue}) {
			t.Fatalf("dequeued %v, want the no-op issue itself", got)
		}
		if s.noOpBlockers[key] {
			t.Error("the hold outlived the issue's own dispatch")
		}
	})
}
