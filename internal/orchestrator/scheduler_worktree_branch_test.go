package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ---------------------------------------------------------------------------
// #299 — the two remaining bare workspace-root branch lookups.
//
// On a worktree-isolated run issue-{N}.json is written INSIDE the worktree, so
// loadFeatureBranch(workspaceRoot, N) answers "" for every such run. #163
// converted three call sites to resolveFeatureBranch (runtime → worktree →
// root); two were left behind, and each fails silently in its own way:
//
//   - the #3873 non-terminal reconcile skipped the branch-PR probe entirely
//     (reconcileIssueResolved only runs the issue-closed check with an empty
//     branch), so a stage that died after its PR merged was recorded
//     success:false and paged falsely;
//   - every V2/V3 history record of a worktree-isolated run lost the branch.
//
// Both fixtures below are worktree-isolated in the shape production produces:
// the issue context exists ONLY in the worktree, written through the package's
// production writer (stagecontext.WriteContext at stagecontext.ContextPath), and
// the runtime learns its worktree the way execution.Manager teaches it
// (Runtime.SetProcess, internal/execution/manager.go).
// ---------------------------------------------------------------------------

// writeIssueContextInWorktree writes issue-{N}.json into the worktree using the
// production context writer and path builder — never a hand-authored JSON body,
// so the artifact under test is exactly the shape the pipeline writes.
func writeIssueContextInWorktree(t *testing.T, worktree string, issueNumber int, repo, branch string) {
	t.Helper()
	ctx := &stagecontext.StageContext{
		IssueNumber: issueNumber,
		Repo:        repo,
		Branch:      branch,
		Stage:       string(state.StageIssuePickup),
	}
	if err := stagecontext.Validate(ctx); err != nil {
		t.Fatalf("fixture issue context is not a valid stage context: %v", err)
	}
	path := stagecontext.ContextPath(worktree, issueNumber, "issue")
	if err := stagecontext.WriteContext(path, ctx); err != nil {
		t.Fatalf("write issue context into worktree: %v", err)
	}
}

// worktreeIsolatedRunner is a stage runner for a worktree-isolated run. It does
// the two things the real execution.Manager does that matter here: it records
// the worktree on the runtime (manager.go SetProcess) and it writes the stage's
// context into the worktree it ran in — never into the workspace root.
type worktreeIsolatedRunner struct {
	t         *testing.T
	mu        sync.Mutex
	calls     map[state.PipelineStage]int
	worktree  string
	repo      string
	branch    string // branch the issue-pickup context names ("" = none resolved)
	failStage state.PipelineStage
}

func newWorktreeIsolatedRunner(t *testing.T, worktree, repo, branch string, failStage state.PipelineStage) *worktreeIsolatedRunner {
	return &worktreeIsolatedRunner{
		t:         t,
		calls:     make(map[state.PipelineStage]int),
		worktree:  worktree,
		repo:      repo,
		branch:    branch,
		failStage: failStage,
	}
}

func (r *worktreeIsolatedRunner) count(stage state.PipelineStage) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[stage]
}

func (r *worktreeIsolatedRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls[params.Stage]++
	r.mu.Unlock()

	// Production parity: the execution manager stamps the worktree at provision
	// time (#399), before any failure exit — not at process registration.
	if params.Runtime != nil {
		params.Runtime.SetWorktree(r.worktree)
	}

	if params.Stage == r.failStage {
		// Pre-flight death shape: non-zero exit, no stage error text, no output.
		return &StageRunResult{ExitCode: 1}, nil
	}

	if params.Stage == state.StageIssuePickup {
		writeIssueContextInWorktree(r.t, r.worktree, params.IssueNumber, r.repo, r.branch)
	}
	return &StageRunResult{ExitCode: 0, InputTokens: 100, OutputTokens: 50}, nil
}

// ghProbeRecorder captures the gh argv the reconcile path shells out with, so a
// test can assert WHICH probes were reachable and with which branch.
type ghProbeRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (g *ghProbeRecorder) record(args []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, append([]string(nil), args...))
}

// prListHead returns the `--head` value of the first `gh pr list` probe, and
// whether that probe happened at all. The two are deliberately separate: "no
// probe" and "probe with an empty branch" are different failures and must never
// collapse into one return value.
func (g *ghProbeRecorder) prListHead() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, args := range g.calls {
		if !ghArgsContain(args, "pr") || !ghArgsContain(args, "list") {
			continue
		}
		for i, a := range args {
			if a == "--head" && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", true
	}
	return "", false
}

// worktreeRunFixture is a real git repo with a real `git worktree` for the run,
// matching the on-disk layout of a worktree-isolated pipeline run.
type worktreeRunFixture struct {
	root     string // workspace root (the main checkout)
	worktree string // the run's isolated worktree
}

func newWorktreeRunFixture(t *testing.T, issueNumber int, branch string) *worktreeRunFixture {
	t.Helper()
	f := newForkFixture(t)
	root := f.work

	for _, dir := range []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	} {
		writeSkillFile(t, root, dir)
	}

	worktree := filepath.Join(root, ".nightgauge", "worktrees", "issue-"+strconv.Itoa(issueNumber))
	gitx(t, root, "worktree", "add", "-b", branch, worktree)

	return &worktreeRunFixture{root: root, worktree: worktree}
}

func newWorktreeRunScheduler(root string, runner StageRunner) *Scheduler {
	return &Scheduler{
		repoRunning:    make(map[string]int),
		mergeLocks:     make(map[string]*sync.Mutex),
		retryEngine:    NewRetryEngine(RetryConfig{MaxBacktracks: 0, MaxEscalationsPerStage: 0}),
		budgetEngine:   NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:    NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:       newMockIssueSvc(),
		execMgr:        execution.NewManager(root, nil),
		stageRunner:    runner,
		budgetRetries:  make(map[string]int),
		workspaceRoot:  root,
		prCreateRunner: alwaysPuntPRCreateRunner{},
	}
}

// TestScheduler_NonTerminalReconcile_WorktreeIsolatedRun_ReachesPRCheck is the
// #299 site-1 reproduction. A worktree-isolated run whose feature-planning stage
// dies in pre-flight, on an issue whose PR already merged: the #3873 reconcile
// must be able to name the branch, because reconcileIssueResolved only runs the
// branch-PR probe when it has one. Pre-fix the lookup read the workspace root —
// where issue-{N}.json does not exist on a worktree run — so the probe was never
// issued and the run was recorded success:false and paged falsely.
//
// The assertion is on the probe, not on a mocked helper: the `gh pr list --head`
// call is the observable proof that the resolved branch reached the forge check.
func TestScheduler_NonTerminalReconcile_WorktreeIsolatedRun_ReachesPRCheck(t *testing.T) {
	const (
		issueNumber = 299
		repo        = "nightgauge/nightgauge"
		branch      = "fix/299-resolve-feature-branch"
	)
	f := newWorktreeRunFixture(t, issueNumber, branch)

	probes := &ghProbeRecorder{}
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		probes.record(args)
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("MERGED", testForeignPRNumber)), nil
	})

	runner := newWorktreeIsolatedRunner(t, f.worktree, repo, branch, state.StageFeaturePlanning)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-299"}
	s.runPipeline(context.Background(), item)

	if got := runner.count(state.StageFeaturePlanning); got != 1 {
		t.Fatalf("feature-planning ran %d times, want 1 — the fixture never reached the failing stage", got)
	}

	// The context this run resolved its branch from lives ONLY in the worktree.
	// If that ever stops being true the test is no longer exercising #299.
	if _, err := os.Stat(stagecontext.ContextPath(f.root, issueNumber, "issue")); err == nil {
		t.Fatalf("fixture leaked issue-%d.json into the workspace root — the worktree-isolation premise is gone", issueNumber)
	}

	head, probed := probes.prListHead()
	if !probed {
		t.Fatalf("the branch-PR probe never ran: the non-terminal reconcile could not name a branch on a worktree-isolated run, "+
			"so a stage that died after its PR merged is recorded success:false (#299 site 1). gh calls seen: %v", probes.calls)
	}
	if head != branch {
		t.Errorf("gh pr list --head = %q, want the run's real branch %q", head, branch)
	}

	// The probe reaching the forge is the mechanism, not the outcome. What #299
	// claims is that the run stops being recorded — and paged on — as a failure,
	// so assert the durable artifact every consumer reads: the history record.
	// (DataAggregator.ts counts outcome=="failed"; DashboardState.ts branches on
	// it.) Resolving the branch is not enough on its own: the pre-flight death
	// wrote no planning context, so the #2870 output-context check re-manufactures
	// the very failure the reconcile just cleared.
	rec := findHistoryRecord(t, f.root, issueNumber)
	if rec.Outcome != "complete" {
		t.Errorf("run recorded outcome=%q terminal_failure_kind=%q, want outcome=%q — the reconcile cleared the stage "+
			"failure but the run was still written as a failure, so the false-failure signal #299 exists to remove is "+
			"still emitted (#299 site 1)", rec.Outcome, rec.TerminalFailureKind, "complete")
	}
	if rec.TerminalFailureKind == TerminalKindValidationError {
		t.Errorf("run recorded terminal_failure_kind=%q — the #2870 output-context check ran on the stage whose failure "+
			"was just reconciled and re-manufactured it (#299 site 1)", rec.TerminalFailureKind)
	}
}

// findHistoryRecord returns today's history record for an issue, failing the
// test when no record was written for it.
func findHistoryRecord(t *testing.T, workspaceRoot string, issueNumber int) state.V2RunRecord {
	t.Helper()
	records := readDailyJSONLRecords(t, workspaceRoot)
	for i := range records {
		if records[i].IssueNumber == issueNumber {
			return records[i]
		}
	}
	t.Fatalf("no history record written for #%d (got %d records)", issueNumber, len(records))
	return state.V2RunRecord{}
}

// TestScheduler_NonTerminalReconcile_UnresolvableBranch_LogsTheSkip pins the
// #299 decision that the skip must never again be silent. When no source can
// name the branch — here the issue context exists but carries none — the branch
// probe is genuinely unavailable, and the pipeline must SAY that instead of
// quietly degrading to the issue-closed check alone.
func TestScheduler_NonTerminalReconcile_UnresolvableBranch_LogsTheSkip(t *testing.T) {
	const (
		issueNumber = 298
		repo        = "nightgauge/nightgauge"
		branch      = "fix/298-worktree"
	)
	// The worktree still exists on a real branch; the CONTEXT names none, which
	// is the only state in which resolveFeatureBranch legitimately answers "".
	f := newWorktreeRunFixture(t, issueNumber, branch)

	probes := &ghProbeRecorder{}
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		probes.record(args)
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(pr("MERGED", testForeignPRNumber)), nil
	})

	runner := newWorktreeIsolatedRunner(t, f.worktree, repo, "", state.StageFeaturePlanning)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-298"}
	out := captureLog(t, func() {
		s.runPipeline(context.Background(), item)
	})

	if _, probed := probes.prListHead(); probed {
		t.Errorf("the branch-PR probe must not run without a branch — gh calls: %v", probes.calls)
	}

	// Assert the composed line, not its fragments: every scheduler log line is
	// `#<issue>: `-prefixed, so a bare `#298` check passes on any output at all
	// and pins nothing.
	const wantSkipLog = "#298: non-terminal stage feature-planning failed but no feature branch could be " +
		"determined from any source — skipping the PR-landed reconcile check"
	if !strings.Contains(out, wantSkipLog) {
		t.Errorf("an unresolvable branch skipped the PR-landed reconcile check SILENTLY (or the line no longer\n"+
			"names the issue and stage it applies to).\nwant substring: %q\nlog was:\n%s", wantSkipLog, out)
	}
}

// TestScheduler_NonTerminalReconcile_IssuePickupFailure_NoSkipLog pins the
// exemption: issue-pickup is the stage that CREATES the branch (SetBranch fires
// immediately after it), so on a failed pickup no branch can exist by
// construction. Logging there would fire the line on every failed pickup and
// train readers to ignore it; it must mean "a branch should exist but nothing
// could name it".
func TestScheduler_NonTerminalReconcile_IssuePickupFailure_NoSkipLog(t *testing.T) {
	const (
		issueNumber = 297
		repo        = "nightgauge/nightgauge"
		branch      = "fix/297-pickup"
	)
	f := newWorktreeRunFixture(t, issueNumber, branch)

	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if ghArgsContain(args, "issue") && ghArgsContain(args, "view") {
			return []byte(`{"state":"OPEN"}`), nil
		}
		return prListPayload(), nil
	})

	runner := newWorktreeIsolatedRunner(t, f.worktree, repo, branch, state.StageIssuePickup)
	s := newWorktreeRunScheduler(f.root, runner)

	item := types.BoardItem{Number: issueNumber, Repo: repo, ID: "item-297"}
	out := captureLog(t, func() {
		s.runPipeline(context.Background(), item)
	})

	// Match on the invariant part of the line rather than its current wording,
	// so a reworded message cannot make this assertion pass vacuously.
	if strings.Contains(out, "skipping the PR-landed reconcile check") {
		t.Errorf("the skip log fired on a failed issue-pickup, where no branch can exist yet; log was:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Site 2 — recordV2History
// ---------------------------------------------------------------------------

// recordHistoryForWorktreeRun drives recordV2History exactly as runPipeline does
// on a worktree-isolated run and returns the record it wrote.
func recordHistoryForWorktreeRun(t *testing.T, workspaceRoot string, snap *state.RuntimeState, item types.BoardItem) state.V2RunRecord {
	t.Helper()
	s := &Scheduler{}
	s.recordV2History(item, snap, true, workspaceRoot, 3, "standard", "", nil, nil)
	return findHistoryRecord(t, workspaceRoot, item.Number)
}

// TestScheduler_RecordV2History_WorktreeIsolatedRun_PersistsRealBranch is the
// #299 site-2 reproduction. Both sources resolveFeatureBranch consults ahead of
// the workspace root are exercised: the live runtime (populated at issue-pickup
// via SetBranch) and the worktree's own issue context. Pre-fix the record read
// only the workspace root, so every worktree-isolated run's V2/V3 record lost
// the branch and BuildV2Record substituted a synthetic `feat/{N}` placeholder —
// a value indistinguishable from a real branch to every downstream reader.
func TestScheduler_RecordV2History_WorktreeIsolatedRun_PersistsRealBranch(t *testing.T) {
	const (
		repo   = "nightgauge/nightgauge"
		branch = "fix/299-resolve-feature-branch"
	)

	t.Run("branch carried on the live runtime", func(t *testing.T) {
		root := t.TempDir()
		worktree := filepath.Join(root, ".nightgauge", "worktrees", "issue-299")
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatalf("create worktree dir: %v", err)
		}
		writeIssueContextInWorktree(t, worktree, 299, repo, branch)

		snap := state.NewRuntimeState(repo, 299, "item-299", testRunID())
		snap.SetWorktree(worktree)
		snap.SetBranch(branch)

		rec := recordHistoryForWorktreeRun(t, root, snap, types.BoardItem{Number: 299, Repo: repo, Title: "t"})
		if rec.Branch != branch {
			t.Errorf("record branch = %q, want %q — the run's history lost the branch it actually ran on", rec.Branch, branch)
		}
	})

	t.Run("branch recoverable only from the worktree context", func(t *testing.T) {
		root := t.TempDir()
		worktree := filepath.Join(root, ".nightgauge", "worktrees", "issue-300")
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatalf("create worktree dir: %v", err)
		}
		const wtBranch = "fix/300-worktree-only"
		writeIssueContextInWorktree(t, worktree, 300, repo, wtBranch)

		// No SetBranch: an adopted / rehydrated run whose runtime never learned
		// the branch still has it on disk, in the worktree the stages ran in.
		snap := state.NewRuntimeState(repo, 300, "item-300", testRunID())
		snap.SetWorktree(worktree)

		rec := recordHistoryForWorktreeRun(t, root, snap, types.BoardItem{Number: 300, Repo: repo, Title: "t"})
		if rec.Branch != wtBranch {
			t.Errorf("record branch = %q, want %q — the worktree's issue context was never consulted", rec.Branch, wtBranch)
		}
	})
}

// TestScheduler_RecordV2History_UnresolvedBranch_KeyPresentEmptyMeansUndetermined
// pins the whole record contract for a run whose branch never resolved:
//
//	the `branch` key is ALWAYS present, and "" is what it says when nothing
//	resolved. A non-empty value therefore means a branch that actually existed.
//
// Both halves matter, and they fail in opposite directions:
//
//   - OMIT the key and "undetermined" stops being expressible. The on-disk
//     contract is key-always-present, so an absent key means a DIFFERENT thing
//     — a pre-#397 record, a foreign producer, or a writer violating this
//     contract — and omitempty would collapse that distinction into the one
//     value ("") that is supposed to mean something specific. Note what this
//     reason is NOT: both TypeScript readers tolerate an absent key as of #397
//     (`branch: z.string().default("")` on the V1/V2 schemas in
//     packages/nightgauge-vscode/src/schemas/executionHistory.ts, V3 extends
//     V2; and DashboardState.importParsedRunRecord's
//     `typeof parsed.branch === "string" ? parsed.branch : ""` coercion), which
//     is precisely why the WRITER has to keep the two shapes apart. Every
//     non-zod consumer — jq, a human reading the JSONL, the index surface, the
//     byte-verbatim fixtures — depends on the same distinction. That is why the
//     json tags below are asserted to have no omitempty.
//   - FABRICATE a value (`feat/{N}`, what BuildV2Record and the
//     orchestrator-crash synthesizer both did before #397) and every reader is
//     told a branch existed. Nothing downstream can tell that apart from a real
//     branch — not the dashboard, not the analytics upload, not a human reading
//     the JSONL — so a run that knew nothing looked exactly like a run that
//     knew. #299 fixed the RESOLUTION (worktree-first lookup); #397 removed the
//     fabrication that made an unresolved branch unfalsifiable.
//
// The log line is asserted too: "" on disk is honest but silent about WHICH run
// could not name its branch, and that is a resolution gap worth seeing.
func TestScheduler_RecordV2History_UnresolvedBranch_KeyPresentEmptyMeansUndetermined(t *testing.T) {
	const repo = "nightgauge/nightgauge"
	root := t.TempDir()

	// Nothing anywhere names a branch: no runtime branch, no worktree, no
	// workspace-root context.
	snap := state.NewRuntimeState(repo, 301, "item-301", testRunID())

	var rec state.V2RunRecord
	out := captureLog(t, func() {
		rec = recordHistoryForWorktreeRun(t, root, snap, types.BoardItem{Number: 301, Repo: repo, Title: "t"})
	})

	// An empty branch on disk names no issue number, so the run log is the only
	// place that says WHICH run failed to resolve one.
	const wantUndeterminedLog = "#301: no feature branch could be determined from any source — the history " +
		"record will carry an EMPTY branch, which is how a record says \"undetermined\"; nothing is " +
		"fabricated in its place (#299, #397)"
	if !strings.Contains(out, wantUndeterminedLog) {
		t.Errorf("the history record recorded an undetermined branch SILENTLY.\nwant substring: %q\nlog was:\n%s",
			wantUndeterminedLog, out)
	}

	// The `branch` json tag must stay exactly that — no omitempty — on BOTH
	// surfaces that carry it. With the fabrication gone the value is ""
	// precisely in this case, so an omitempty added to either would start
	// DROPPING the key on exactly the records this test covers. The index entry
	// is the surface with no zod safety net at all: HistoryIndexEntry is a plain
	// TypeScript interface, so an absent key reaches
	// DashboardState.indexEntryToRunSummary and
	// LocalAuditFallbackService.entryToAuditLogEntry as `undefined` on a field
	// typed `string`.
	for _, surface := range []struct {
		name string
		typ  reflect.Type
	}{
		{"state.V2RunRecord", reflect.TypeOf(state.V2RunRecord{})},
		{"state.V2IndexEntry", reflect.TypeOf(state.V2IndexEntry{})},
	} {
		branchField, ok := surface.typ.FieldByName("Branch")
		if !ok {
			t.Fatalf("%s has no Branch field — the readers named above key off `branch`", surface.name)
		}
		if got := branchField.Tag.Get("json"); got != "branch" {
			t.Errorf("%s.Branch json tag = %q, want exactly %q (internal/state/history.go): any "+
				"omitempty here drops the key on every undetermined-branch record", surface.name, got, "branch")
		}
	}

	if rec.Branch != "" {
		t.Errorf("unresolved branch recorded as %q, want %q — nothing named a branch for this run, and a "+
			"non-empty value is indistinguishable from one that really existed (#397)", rec.Branch, "")
	}

	// Assert on the serialized BYTES, not the decoded map: only the raw line can
	// show key-present-and-empty as a single fact. A decoded map reports the
	// same `""` whether the key was written empty or omitted entirely.
	line := readRawHistoryLineText(t, root, 301)
	if !strings.Contains(line, `"branch":""`) {
		t.Errorf("history record did not serialize `\"branch\":\"\"` (key present, value empty — the "+
			"undetermined marker).\nline was:\n%s", line)
	}
	raw := readRawHistoryLine(t, root, 301)
	if _, ok := raw["branch"]; !ok {
		t.Errorf("history record omitted the `branch` key; on disk an absent key means a record from " +
			"before this contract or from another producer, not \"undetermined\"")
	}
}

// readRawHistoryLine returns today's history record for an issue as a raw JSON
// object, so a test can assert on key PRESENCE rather than on the decoded
// struct (which cannot distinguish an absent key from an empty string).
func readRawHistoryLine(t *testing.T, workspaceRoot string, issueNumber int) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(readRawHistoryLineText(t, workspaceRoot, issueNumber)), &raw); err != nil {
		t.Fatalf("decode raw history line for #%d: %v", issueNumber, err)
	}
	return raw
}

// readRawHistoryLineText returns today's history record for an issue as the
// verbatim JSONL line the writer emitted. Decoding loses the one distinction
// this file's contract turns on — a key written empty versus a key omitted —
// so assertions about key presence combined with an empty value must read the
// bytes.
func readRawHistoryLineText(t *testing.T, workspaceRoot string, issueNumber int) string {
	t.Helper()
	dir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		for _, line := range splitJSONLines(data) {
			if len(line) == 0 {
				continue
			}
			var raw map[string]any
			if json.Unmarshal(line, &raw) != nil {
				continue
			}
			if n, ok := raw["issue_number"].(float64); ok && int(n) == issueNumber {
				return string(line)
			}
		}
	}
	t.Fatalf("no raw history line for #%d", issueNumber)
	return ""
}
