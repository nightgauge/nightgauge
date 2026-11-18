package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

const closeCrit = "`ExcludePattern` keeps its current value, so the pinned test in `scan/scan_test.go` still passes unchanged"

func closeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gittest.Run(t, dir, args...)
}

func closeWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mergedPartialRepo builds the earlier partial PR as a real merge commit that
// changes only a sibling of `ExcludePattern`, and returns the repo and the
// merge SHA. editSpan makes the merge edit the constant's line instead.
func mergedPartialRepo(t *testing.T, editSpan bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	closeGit(t, dir, "init", "-q", "-b", "main")
	closeGit(t, dir, "config", "user.email", "test@example.com")
	closeGit(t, dir, "config", "user.name", "Test")
	closeGit(t, dir, "config", "commit.gpgsign", "false")
	base := "package scan\n\nconst ExcludePattern = `^---`\n\nfunc match(s string) bool {\n\treturn s != \"\"\n}\n"
	closeWrite(t, dir, "scan/scan.go", base)
	closeWrite(t, dir, "scan/scan_test.go", "package scan\n")
	closeGit(t, dir, "add", "-A")
	closeGit(t, dir, "commit", "-qm", "base")
	closeGit(t, dir, "checkout", "-q", "-b", "feat")
	changed := strings.Replace(base, "return s != \"\"", "return len(s) > 0", 1)
	if editSpan {
		changed = strings.Replace(base, "`^---`", "`^\\+\\+\\+`", 1)
	}
	closeWrite(t, dir, "scan/scan.go", changed)
	closeGit(t, dir, "commit", "-qam", "change")
	closeGit(t, dir, "checkout", "-q", "main")
	closeWrite(t, dir, "README.md", "x\n")
	closeGit(t, dir, "add", "-A")
	closeGit(t, dir, "commit", "-qm", "main")
	closeGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feat")
	return dir, closeGit(t, dir, "rev-parse", "HEAD")
}

func TestJudgeRemainder(t *testing.T) {
	t.Parallel()
	dir, merge := mergedPartialRepo(t, false)
	if v := JudgeRemainder(context.Background(), dir, merge, []string{closeCrit}); !v.AllMet || len(v.Criteria) != 1 {
		t.Errorf("preservation over a sibling-only merge: %+v, want all met", v)
	}
	v := JudgeRemainder(context.Background(), dir, merge, []string{closeCrit, "Add `matchHeader` to `scan/scan.go`"})
	if v.AllMet || len(v.Unmet()) != 1 || v.Unmet()[0].Text != "Add `matchHeader` to `scan/scan.go`" {
		t.Errorf("one presence criterion: %+v, want exactly it unmet", v)
	}
	for name, sha := range map[string]string{
		"no merge SHA":      "",
		"not a SHA":         "main",
		"missing in object": strings.Repeat("a", 40),
	} {
		v := JudgeRemainder(context.Background(), dir, sha, []string{closeCrit})
		if v.AllMet || len(v.Unmet()) != 1 || !strings.HasPrefix(v.Unmet()[0].Evidence, "undecidable:") {
			t.Errorf("%s: %+v, want the criterion unmet as undecidable", name, v)
		}
	}
	if v := JudgeRemainder(context.Background(), dir, merge, nil); v.AllMet {
		t.Errorf("no criteria: %+v, want not all met", v)
	}

	dir, merge = mergedPartialRepo(t, true)
	if v := JudgeRemainder(context.Background(), dir, merge, []string{closeCrit}); v.AllMet {
		t.Errorf("merge edited the span: %+v, want unmet", v)
	}
}

type fakeCloseIssues struct {
	comments   []string
	edits      []string
	closed     []string
	commentErr error
	editErr    error
	closeErr   error
}

func (f *fakeCloseIssues) AddComment(_ context.Context, id, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, body)
	return nil
}

func (f *fakeCloseIssues) EditIssue(_ context.Context, id, body string) (*types.Issue, error) {
	if f.editErr != nil {
		return nil, f.editErr
	}
	f.edits = append(f.edits, body)
	return &types.Issue{}, nil
}

func (f *fakeCloseIssues) CloseIssue(_ context.Context, id string) error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, id)
	return nil
}

type fakeCloseBoard struct{ statuses []string }

func (b *fakeCloseBoard) SyncStatus(_ context.Context, _, _ string, _ int, status string) error {
	b.statuses = append(b.statuses, status)
	return nil
}

func (b *fakeCloseBoard) AddIssueByNumber(context.Context, string, string, int) (string, error) {
	return "", nil
}

func metVerdict() RemainderVerdict {
	return RemainderVerdict{MergeSHA: strings.Repeat("b", 40), AllMet: true, Criteria: []CriterionVerdict{
		{Text: closeCrit, Met: true, Evidence: "the diff changes no line containing `ExcludePattern`"},
	}}
}

func TestCloseMetRemainder(t *testing.T) {
	t.Parallel()
	issues, board := &fakeCloseIssues{}, &fakeCloseBoard{}
	deps := PartialCloseDeps{Issues: issues, Board: board, Owner: "o", Repo: "r", ProjectNumber: 5}
	body := "## Acceptance criteria\n\n- [x] Part A is done\n- [ ] " + closeCrit + "\n- [ ] Unrelated\n"
	target := PartialCloseTarget{Number: 3423, NodeID: "I_1", Body: body, PR: 3422}
	res, err := CloseMetRemainder(context.Background(), deps, target, metVerdict())
	if err != nil || !res.Closed || !res.Board.Done() {
		t.Fatalf("all met: res=%+v err=%v, want closed with the board Done", res, err)
	}
	if len(issues.comments) != 1 || !strings.HasPrefix(issues.comments[0], PartialRemainderClosedMarker) ||
		!strings.Contains(issues.comments[0], "ExcludePattern") {
		t.Errorf("evidence comment: %q", issues.comments)
	}
	if len(issues.edits) != 1 || !strings.Contains(issues.edits[0], "- [x] "+closeCrit) ||
		!strings.Contains(issues.edits[0], "- [ ] Unrelated") {
		t.Errorf("ticks: %q, want only the met criterion ticked", issues.edits)
	}
	if len(board.statuses) != 1 || board.statuses[0] != string(state.StatusDone) {
		t.Errorf("board: %v, want Done", board.statuses)
	}

	notMet := metVerdict()
	notMet.AllMet = false
	notMet.Criteria[0].Met = false
	issues = &fakeCloseIssues{}
	deps.Issues = issues
	if _, err := CloseMetRemainder(context.Background(), deps, target, notMet); !errors.Is(err, ErrRemainderNotMet) ||
		len(issues.comments)+len(issues.closed) != 0 {
		t.Errorf("not all met: err=%v, comments=%d closed=%d — want refused with no writes", err, len(issues.comments), len(issues.closed))
	}
	if _, err := CloseMetRemainder(context.Background(), deps, PartialCloseTarget{Number: 1}, metVerdict()); !errors.Is(err, ErrNoIssueToClose) {
		t.Errorf("no node id: err=%v", err)
	}
	if _, err := CloseMetRemainder(context.Background(), PartialCloseDeps{}, target, metVerdict()); !errors.Is(err, ErrNoCloseDeps) {
		t.Errorf("no issue service: err=%v", err)
	}
	issues = &fakeCloseIssues{closeErr: errors.New("forge down")}
	deps.Issues = issues
	if res, err := CloseMetRemainder(context.Background(), deps, target, metVerdict()); err == nil || res.Closed ||
		len(issues.comments) != 1 {
		t.Errorf("close failed: res=%+v err=%v, want an error after the comment and not closed", res, err)
	}
	issues = &fakeCloseIssues{commentErr: errors.New("forge down")}
	deps.Issues = issues
	if _, err := CloseMetRemainder(context.Background(), deps, target, metVerdict()); err == nil ||
		len(issues.edits)+len(issues.closed) != 0 {
		t.Errorf("comment failed: err=%v edits=%d closed=%d, want an error with no tick and no close", err, len(issues.edits), len(issues.closed))
	}
	issues = &fakeCloseIssues{editErr: errors.New("edit refused")}
	res, err = CloseMetRemainder(context.Background(), PartialCloseDeps{Issues: issues}, target, metVerdict())
	if err != nil || !res.Closed || len(res.Ticked) != 0 || res.Board != hooks.BoardSyncNotAttempted {
		t.Errorf("tick failed, no board: res=%+v err=%v, want closed, nothing ticked, board not attempted", res, err)
	}
}

// AC 9: a model's claim never closes. dev-{N}.json and the stage output may
// say every criterion is met; only the deterministic verdict decides.
func TestPartialNoOp_ModelClaimNeverCloses(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	closeGit(t, ws, "init", "-q")
	devPath, err := stagecontext.ContextPath(ws, 3423, "dev")
	if err != nil {
		t.Fatal(err)
	}
	closeWrite(t, filepath.Dir(devPath), filepath.Base(devPath),
		`{"files_changed":{"created":[],"modified":[],"deleted":[]},"notes":"every remaining criterion is already met","criteria_met":true}`)
	scope := &state.PartialRerun{PR: 3422, Criteria: []string{closeCrit}}
	claimed := RemainderVerdict{Criteria: []CriterionVerdict{{Text: closeCrit, Evidence: "undecidable: the partial PR's merge commit is unknown"}}}
	got := DecidePartialNoOp(ws, 3423, scope, false, claimed)
	if got.Action != PartialNoOpActionBook || !strings.Contains(got.Detail, "undecidable") {
		t.Fatalf("model claims met, verdict not met: %+v, want book naming the unmet criterion", got)
	}
	issues := &fakeCloseIssues{}
	if _, err := CloseMetRemainder(context.Background(), PartialCloseDeps{Issues: issues},
		PartialCloseTarget{Number: 3423, NodeID: "I_1", PR: 3422}, claimed); err == nil || len(issues.closed) != 0 {
		t.Fatalf("close on a not-met verdict: err=%v closed=%v", err, issues.closed)
	}
	if got.Remainder == nil || got.Remainder.AllMet {
		t.Fatalf("book carries remainder %+v, want the not-met verdict", got.Remainder)
	}
	if got := DecidePartialNoOp(ws, 3423, scope, false, metVerdict()); got.Action != PartialNoOpActionClose ||
		got.Remainder == nil || !got.Remainder.AllMet {
		t.Fatalf("deterministic all-met verdict: %+v, want close carrying the verdict", got)
	}
	if got := DecidePartialNoOp(ws, 3423, nil, false, metVerdict()); got.Remainder != nil {
		t.Fatalf("not scoped carries remainder %+v, want none", got.Remainder)
	}
}

func TestDecidePartialNoOp_Close(t *testing.T) {
	t.Parallel()
	scope := &state.PartialRerun{PR: 3422, Criteria: []string{closeCrit}}
	if d := decidePartialNoOp(3423, scope, operatorRevision{}, "Task 2 is wrong", false, metVerdict()); d.Action != partialNoOpDispute {
		t.Errorf("dispute with an all-met verdict: %v, want dispute", d.Action)
	}
	if d := decidePartialNoOp(3423, scope, operatorRevision{}, "", false, metVerdict()); d.Action != partialNoOpClose ||
		!strings.Contains(d.Detail, "closed as completed") {
		t.Errorf("all met: %+v, want close", d)
	}
	if d := decidePartialNoOp(3423, nil, operatorRevision{}, "", false, metVerdict()); d.Action != partialNoOpNotScoped {
		t.Errorf("unscoped: %v, want not scoped", d.Action)
	}
	if d := decidePartialNoOp(3423, scope, operatorRevision{}, "", false, RemainderVerdict{AllMet: true}); d.Action != partialNoOpBook {
		t.Errorf("all met with no criteria: %v, want book", d.Action)
	}
}

// The scheduler ends a met remainder issue_closed (a non-failure), and a
// failed close not_pipeline_actionable with the shared detail.
func TestEndPartialRemainderMet(t *testing.T) {
	t.Parallel()
	item := types.BoardItem{Repo: "o/r", Number: 3423}
	s := &Scheduler{partialRemainderClose: func(context.Context, types.BoardItem, int, RemainderVerdict) (PartialCloseResult, error) {
		return PartialCloseResult{Closed: true}, nil
	}}
	if detail, kind := s.endPartialRemainderMet(context.Background(), item, 3422, metVerdict()); kind != TerminalKindIssueClosed ||
		!strings.Contains(detail, "#3423") {
		t.Errorf("closed: kind=%s detail=%q", kind, detail)
	}
	s.partialRemainderClose = func(context.Context, types.BoardItem, int, RemainderVerdict) (PartialCloseResult, error) {
		return PartialCloseResult{}, errors.New("forge down")
	}
	if detail, kind := s.endPartialRemainderMet(context.Background(), item, 3422, metVerdict()); kind != TerminalKindNotPipelineActionable ||
		detail != PartialCloseFailedDetail(3423, 3422, errors.New("forge down")) {
		t.Errorf("close failed: kind=%s detail=%q", kind, detail)
	}
}

// judgePartialRemainder reads the merge SHA from the record written at scope
// time, and never judges a record naming another PR.
func TestJudgePartialRemainder_ReadsRecord(t *testing.T) {
	t.Parallel()
	dir, merge := mergedPartialRepo(t, false)
	scope := &state.PartialRerun{PR: 3422, Criteria: []string{closeCrit}}
	if err := WritePartialDeliveryContext(dir, 3423, PartialDeliveryFile{PR: 3422, Criteria: scope.Criteria, MergeSHA: merge}); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{}
	if v := s.judgePartialRemainder(context.Background(), scope, 3423, dir); !v.AllMet || v.MergeSHA != merge {
		t.Errorf("record with the merge SHA: %+v, want all met", v)
	}
	if v := s.judgePartialRemainder(context.Background(), &state.PartialRerun{PR: 99, Criteria: scope.Criteria}, 3423, dir); v.AllMet {
		t.Errorf("record names another PR: %+v, want not met", v)
	}
	if v := s.judgePartialRemainder(context.Background(), nil, 3423, dir); v.AllMet || len(v.Criteria) != 0 {
		t.Errorf("unscoped: %+v", v)
	}
}
