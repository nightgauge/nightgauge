package stages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	"github.com/nightgauge/nightgauge/internal/filing"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// review2757 is the shape of the comment #2757 was enqueued past: a correctness
// major anchored on README.md, naming an install ref that does not exist.
const review2757 = `<!-- nightgauge:pr-review -->
## Nightgauge review

0 blocker, 2 major, 1 nitpick.

- **major** (correctness) ` + "`README.md:161`" + ` — The install command names a ref that does not exist.
  - Fix: Use the released tag.
    Then verify it.
- **major** (security) ` + "`internal/x/y.go:9`" + ` — Token logged at debug level.
  - Fix: Remove the log line.
- **nitpick** (correctness) ` + "`a.go:1`" + ` — Rename this.
  - Fix: Rename it.

_Advisory review of a pull request the pipeline did not author; it does not block merging._
`

const reviewFileOnly = `<!-- nightgauge:pr-review -->
## Nightgauge review

0 blocker, 1 major, 0 nitpick.

- **major** (security) ` + "`internal/x/y.go:9`" + ` — Token logged at debug level.
  - Fix: Remove the log line.
`

const reviewClean = `<!-- nightgauge:pr-review -->
## Nightgauge review

No findings from the correctness, security and plan-compliance passes.
`

func TestParseReviewComment_2757Fixture(t *testing.T) {
	got, err := ParseReviewComment(review2757)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("parsed %d findings, want 3: %+v", len(got), got)
	}
	if got[0].Path() != "README.md" || got[0].Pass != "correctness" || got[0].Severity != "major" {
		t.Errorf("first finding = %+v", got[0])
	}
	if got[0].Fix != "Use the released tag.\nThen verify it." {
		t.Errorf("multi-line fix = %q", got[0].Fix)
	}
}

func TestParseReviewComment_CleanIsEmpty(t *testing.T) {
	got, err := ParseReviewComment(reviewClean)
	if err != nil || len(got) != 0 {
		t.Fatalf("clean comment = %v, %v", got, err)
	}
}

// Format drift has to refuse, never read as clean (the plan's first risk).
func TestParseReviewComment_DriftIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"no marker":       "## Nightgauge review\n\nNo findings from the correctness, security and plan-compliance passes.\n",
		"no count line":   reviewCommentMarker + "\n## Nightgauge review\n\nSomething new.\n",
		"bullets dropped": reviewCommentMarker + "\n\n0 blocker, 2 major, 0 nitpick.\n\n* major in README.md\n",
		"blocker missing": reviewCommentMarker + "\n\n1 blocker, 0 major, 0 nitpick.\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseReviewComment(body); err == nil {
				t.Fatalf("want an error, got %+v", got)
			}
		})
	}
}

// Nitpicks over the renderer's cap are omitted from the bullets on purpose.
func TestParseReviewComment_OmittedNitpicksAreNotDrift(t *testing.T) {
	body := reviewCommentMarker + "\n\n0 blocker, 0 major, 7 nitpick.\n\n- **nitpick** (correctness) `a.go:1` — x\n  - Fix: y\n\n6 more nitpicks omitted.\n"
	if _, err := ParseReviewComment(body); err != nil {
		t.Fatal(err)
	}
}

func TestReviewFindingClass(t *testing.T) {
	for _, tc := range []struct {
		sev, pass, where string
		want             ReviewClass
	}{
		{"blocker", "security", "internal/x.go:1", ReviewClassHold},
		{"blocker", "correctness", "a.go", ReviewClassHold},
		{"major", "correctness", "README.md:161", ReviewClassHold},
		{"major", "correctness", "packages/nightgauge/README.md:3", ReviewClassHold},
		{"major", "correctness", "docs/GETTING_STARTED.md:9", ReviewClassHold},
		{"major", "correctness", "packages/nightgauge/package.json:12", ReviewClassHold},
		{"major", "security", "README.md:161", ReviewClassFile},
		{"major", "plan-compliance", "docs/x.md", ReviewClassFile},
		{"major", "correctness", "internal/docs/x.go:1", ReviewClassFile},
		{"major", "correctness", "packages/a/b/package.json:1", ReviewClassFile},
		{"nitpick", "correctness", "README.md:1", ReviewClassIgnore},
	} {
		got := ReviewFinding{Severity: tc.sev, Pass: tc.pass, Where: tc.where}.Class()
		if got != tc.want {
			t.Errorf("%s %s %s = %s, want %s", tc.sev, tc.pass, tc.where, got, tc.want)
		}
	}
}

func TestDecideEnqueue(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	hold := []ReviewFinding{{Severity: "blocker", Pass: "correctness", Where: "a.go"}}
	file := []ReviewFinding{{Severity: "major", Pass: "security", Where: "a.go"}}
	at := func(ago time.Duration) *time.Time { t := now.Add(-ago); return &t }

	for name, tc := range map[string]struct {
		findings  []ReviewFinding
		ledger    ReviewLedger
		commentAt time.Time
		want      ReviewVerdict
	}{
		"clean":          {nil, ReviewLedger{}, now, ReviewEnqueue},
		"file only":      {file, ReviewLedger{}, now, ReviewEnqueue},
		"hold first":     {hold, ReviewLedger{}, now, ReviewFixRound},
		"hold second":    {hold, ReviewLedger{Rounds: 1}, now, ReviewFixRound},
		"hold at cap":    {hold, ReviewLedger{Rounds: MaxReviewFixRounds}, now, ReviewHoldAtCap},
		"file at cap":    {file, ReviewLedger{Rounds: MaxReviewFixRounds}, now, ReviewEnqueue},
		"open round":     {nil, ReviewLedger{Rounds: 1, InProgress: true, StartedAt: at(time.Minute)}, now, ReviewWaitRound},
		"expired round":  {nil, ReviewLedger{Rounds: 1, InProgress: true, StartedAt: at(61 * time.Minute)}, now, ReviewEnqueue},
		"undated round":  {nil, ReviewLedger{Rounds: 1, InProgress: true}, now, ReviewEnqueue},
		"answered":       {hold, ReviewLedger{Rounds: 1, EndedAt: at(time.Minute)}, now.Add(-time.Hour), ReviewEnqueue},
		"answered, cap":  {hold, ReviewLedger{Rounds: 2, EndedAt: at(time.Minute)}, now.Add(-time.Hour), ReviewEnqueue},
		"reviewed again": {hold, ReviewLedger{Rounds: 1, EndedAt: at(time.Hour)}, now.Add(-time.Minute), ReviewFixRound},
	} {
		t.Run(name, func(t *testing.T) {
			if got := DecideEnqueue(tc.findings, tc.ledger, tc.commentAt, now); got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
}

// reviewGh is a merge-capable client that serves one review comment.
type reviewGh struct {
	fakeGh
	comment ReviewComment
	missing bool
	readErr error
}

func (g *reviewGh) ReviewComment(context.Context, int) (ReviewComment, bool, error) {
	if g.readErr != nil {
		return ReviewComment{}, false, g.readErr
	}
	return g.comment, !g.missing, nil
}

func newReviewGh(body string, at time.Time) *reviewGh {
	clean := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"}
	return &reviewGh{
		fakeGh:  fakeGh{preMerge: clean, postMerge: PRViewSnapshot{State: "MERGED"}},
		comment: ReviewComment{Body: body, UpdatedAt: at},
	}
}

// fakeFiling files every record as a new issue and remembers what it saw.
type fakeFiling struct {
	calls    int
	records  []discoveredwork.Record
	merges   func() int
	mergesAt int
	err      error
	report   bool
}

func (f *fakeFiling) file(_ context.Context, _, _ string, c *discoveredwork.Capture) (*followup.Result, error) {
	f.calls++
	if f.merges != nil {
		f.mergesAt = f.merges()
	}
	f.records = append(f.records, c.Records...)
	res := &followup.Result{Issue: c.IssueNumber, Gate: filing.Gate{Mode: filing.ModeFile}}
	for i, rec := range c.Records {
		it := followup.Item{ID: rec.ID, Outcome: followup.OutcomeFiled, IssueNumber: 900 + i}
		if f.report {
			it = followup.Item{ID: rec.ID, Outcome: followup.OutcomeWouldFile}
			res.ReportOnly, res.Reason = true, "follow_up_filing.mode defaults to report"
		}
		if f.err != nil && i > 0 {
			break
		}
		res.Items = append(res.Items, it)
	}
	return res, f.err
}

func newReviewRunner(t *testing.T, g *reviewGh, f *fakeFiling) (*DeterministicRunner, string) {
	t.Helper()
	root := layouttest.Repo(t)
	r := newRunnerWith(g, 7)
	r.knowledgeConformance = nil
	r.steeringGate = nil
	if f != nil {
		f.merges = func() int { return g.mergeCalls }
		r.SetReviewFiling(f.file)
	}
	return r, root
}

func TestRun_NoReviewCommentMergesAsBefore(t *testing.T) {
	g := newReviewGh("", time.Now())
	g.missing = true
	r, root := newReviewRunner(t, g, nil)
	res, err := r.Run(context.Background(), 100, "o/r", root)
	if err != nil || res.Path != PathMerged {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestRun_CleanReviewMerges(t *testing.T) {
	g := newReviewGh(reviewClean, time.Now())
	r, root := newReviewRunner(t, g, nil)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
}

// AC 10 and the #2757 regression: a green, CLEAN PR must not merge past a
// correctness major on the README, and it goes to the LLM for a fix round.
func TestRun_2757FixtureHoldsTheEnqueue(t *testing.T) {
	g := newReviewGh(review2757, time.Now())
	f := &fakeFiling{}
	r, root := newReviewRunner(t, g, f)
	res, err := r.Run(context.Background(), 100, "o/r", root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != PathPunt || !strings.HasPrefix(res.Reason, ReasonReviewFixNeeded) {
		t.Fatalf("res = %+v, want a %s punt", res, ReasonReviewFixNeeded)
	}
	if g.mergeCalls != 0 {
		t.Fatalf("merged %d time(s) past a hold finding", g.mergeCalls)
	}
	if f.calls != 0 {
		t.Fatalf("filed while holding: the round may answer those findings")
	}
}

// AC 4 and AC 11: a green PR with only file findings enqueues, after its
// findings are filed and never the other way round.
func TestRun_FileOnlyFilesThenMerges(t *testing.T) {
	g := newReviewGh(reviewFileOnly, time.Now())
	f := &fakeFiling{}
	r, root := newReviewRunner(t, g, f)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
	if f.calls != 1 || len(f.records) != 1 {
		t.Fatalf("filed %d record(s) in %d call(s), want 1 in 1", len(f.records), f.calls)
	}
	if f.mergesAt != 0 || g.mergeCalls != 1 {
		t.Fatalf("filing saw %d merge(s) already made; the issue must exist first", f.mergesAt)
	}
	rec := f.records[0]
	if err := discoveredwork.Validate(&rec); err != nil {
		t.Fatalf("record is not valid for the materializer: %v", err)
	}
	if !strings.Contains(rec.Summary, "PR #7") || !strings.Contains(rec.Summary, "internal/x/y.go") {
		t.Errorf("summary = %q", rec.Summary)
	}
	l, err := LoadReviewLedger(root, 100)
	if err != nil || l.Filed[rec.ID] != 900 {
		t.Fatalf("ledger = %+v, %v", l, err)
	}
	if l.Rounds != 0 {
		t.Errorf("a file-only PR spent %d round(s)", l.Rounds)
	}
}

// A retry after a merge failure must not file twice.
func TestRun_RetryDoesNotDoubleFile(t *testing.T) {
	g := newReviewGh(reviewFileOnly, time.Now())
	g.mergeErr = errors.New("transient")
	f := &fakeFiling{}
	r, root := newReviewRunner(t, g, f)
	if res, _ := r.Run(context.Background(), 100, "o/r", root); res.Path != PathPunt {
		t.Fatalf("first attempt = %+v", res)
	}
	g.mergeErr = nil
	if res, _ := r.Run(context.Background(), 100, "o/r", root); res.Path != PathMerged {
		t.Fatalf("second attempt = %+v", res)
	}
	if f.calls != 1 {
		t.Fatalf("filer called %d times across a retry, want 1", f.calls)
	}
}

func TestRun_FilingFailureRefusesTheMerge(t *testing.T) {
	g := newReviewGh(reviewFileOnly, time.Now())
	f := &fakeFiling{err: errors.New("board unavailable")}
	r, root := newReviewRunner(t, g, f)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonReviewFilingFailed) {
		t.Fatalf("res = %+v", res)
	}
	if g.mergeCalls != 0 {
		t.Fatal("merged past a finding it could not track")
	}
}

func TestRun_NoFilerRefusesRatherThanDropsAFinding(t *testing.T) {
	g := newReviewGh(reviewFileOnly, time.Now())
	r, root := newReviewRunner(t, g, nil)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonReviewFilingFailed) {
		t.Fatalf("res = %+v", res)
	}
}

// Report mode is the operator's choice: the merge goes on and the finding lands
// in the capture the post-merge stage reports.
func TestRun_ReportModeRecordsAndMerges(t *testing.T) {
	g := newReviewGh(reviewFileOnly, time.Now())
	f := &fakeFiling{report: true}
	r, root := newReviewRunner(t, g, f)
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
	dir := pipelineContextPath(root, "")
	capture, err := discoveredwork.Load(dir, 100)
	if err != nil || capture == nil || len(capture.Records) != 1 {
		t.Fatalf("capture = %+v, %v", capture, err)
	}
}

func TestRun_HoldAtCapRefuses(t *testing.T) {
	g := newReviewGh(review2757, time.Now())
	r, root := newReviewRunner(t, g, &fakeFiling{})
	if err := saveReviewLedger(root, 100, ReviewLedger{Rounds: MaxReviewFixRounds}); err != nil {
		t.Fatal(err)
	}
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonReviewRoundCap) {
		t.Fatalf("res = %+v, want refused %s; a third round must be impossible", res, ReasonReviewRoundCap)
	}
}

func TestRun_UnparseableReviewRefuses(t *testing.T) {
	g := newReviewGh(reviewCommentMarker+"\nsomething else\n", time.Now())
	r, root := newReviewRunner(t, g, &fakeFiling{})
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathRefused || !strings.HasPrefix(res.Reason, ReasonReviewUnparseable) {
		t.Fatalf("res = %+v", res)
	}
}

func TestRun_ReviewReadFailurePunts(t *testing.T) {
	g := newReviewGh(reviewClean, time.Now())
	g.readErr = errors.New("HTTP 502")
	r, root := newReviewRunner(t, g, &fakeFiling{})
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathPunt || !strings.HasPrefix(res.Reason, ReasonReviewReadFailed) {
		t.Fatalf("res = %+v", res)
	}
}

func TestRun_ReviewRateLimitDefers(t *testing.T) {
	g := newReviewGh(reviewClean, time.Now())
	g.readErr = errors.New("API rate limit exceeded")
	r, root := newReviewRunner(t, g, &fakeFiling{})
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathPunt || res.Reason != ReasonRateLimited {
		t.Fatalf("res = %+v", res)
	}
}

// AC 9: a review that lands while a round is open must not enqueue the old head.
func TestRun_ReviewArrivingDuringARoundHoldsTheEnqueue(t *testing.T) {
	now := time.Now()
	for name, g := range map[string]*reviewGh{
		"clean review": newReviewGh(reviewClean, now),
		"no review":    func() *reviewGh { g := newReviewGh("", now); g.missing = true; return g }(),
	} {
		t.Run(name, func(t *testing.T) {
			r, root := newReviewRunner(t, g, &fakeFiling{})
			if _, err := BeginReviewRound(root, 100, now); err != nil {
				t.Fatal(err)
			}
			res, _ := r.Run(context.Background(), 100, "o/r", root)
			if res.Path != PathRefused || res.Reason != ReasonReviewFixInProgress {
				t.Fatalf("res = %+v", res)
			}
			if g.mergeCalls != 0 {
				t.Fatal("enqueued during a fix round")
			}
		})
	}
}

// AC 9, the second half: the finished round's head goes through, even though the
// comment has not caught up with it.
func TestRun_FinishedRoundEnqueuesTheNewHead(t *testing.T) {
	now := time.Now()
	g := newReviewGh(review2757, now.Add(-time.Hour))
	f := &fakeFiling{}
	r, root := newReviewRunner(t, g, f)
	if _, err := BeginReviewRound(root, 100, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := EndReviewRound(root, 100, now); err != nil {
		t.Fatal(err)
	}
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
}

// A stage killed mid-round must not wedge the PR.
func TestRun_ExpiredRoundDoesNotWedgeThePR(t *testing.T) {
	now := time.Now()
	g := newReviewGh(reviewClean, now)
	r, root := newReviewRunner(t, g, nil)
	if _, err := BeginReviewRound(root, 100, now.Add(-2*reviewRoundExpiry)); err != nil {
		t.Fatal(err)
	}
	res, _ := r.Run(context.Background(), 100, "o/r", root)
	if res.Path != PathMerged {
		t.Fatalf("res = %+v", res)
	}
}

func TestBeginReviewRound_CapIsHard(t *testing.T) {
	root := layouttest.Repo(t)
	now := time.Now()
	for i := 1; i <= MaxReviewFixRounds; i++ {
		l, err := BeginReviewRound(root, 5, now)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if l.Rounds != i {
			t.Fatalf("rounds = %d, want %d", l.Rounds, i)
		}
		if _, err := BeginReviewRound(root, 5, now); err == nil {
			t.Fatal("a second round opened beside an open one")
		}
		if _, err := EndReviewRound(root, 5, now); err != nil {
			t.Fatal(err)
		}
	}
	_, err := BeginReviewRound(root, 5, now)
	if err == nil || !strings.Contains(err.Error(), ReasonReviewRoundCap) {
		t.Fatalf("round past the cap: %v", err)
	}
	// A killed stage's round counts: expiry frees the PR, not the cap.
	l, _ := LoadReviewLedger(root, 5)
	if l.Rounds != MaxReviewFixRounds {
		t.Fatalf("rounds = %d", l.Rounds)
	}
}

func TestEndReviewRound_NothingOpen(t *testing.T) {
	if _, err := EndReviewRound(layouttest.Repo(t), 5, time.Now()); err == nil {
		t.Fatal("closed a round that was never opened")
	}
}

func TestReviewGate_ReachableForTheLLMStage(t *testing.T) {
	g := newReviewGh(review2757, time.Now())
	r, root := newReviewRunner(t, g, &fakeFiling{})
	out := r.ReviewGate(context.Background(), 100, root)
	if out.Proceed || out.Verdict != ReviewFixRound || out.PRNumber != 7 || len(out.Hold) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Hold[0].Path() != "README.md" {
		t.Errorf("hold = %+v", out.Hold[0])
	}
}

func TestLastReviewComment(t *testing.T) {
	out := []byte(`{"body":"old","updated_at":"2026-01-01T00:00:00Z"}
{"body":"new\nline","updated_at":"2026-01-02T00:00:00Z"}
`)
	c, ok, err := lastReviewComment(out)
	if err != nil || !ok || c.Body != "new\nline" {
		t.Fatalf("got %+v %v %v", c, ok, err)
	}
	if _, ok, err := lastReviewComment(nil); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	if _, _, err := lastReviewComment([]byte("{not json")); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestReviewRecord_SurvivesHostileModelOutput(t *testing.T) {
	f := ReviewFinding{Severity: "major", Pass: "security", Where: "../../etc/passwd:1",
		Claim: strings.Repeat("x", 400) + "‮\u0007"}
	rec := reviewRecord(7, 100, f, time.Now())
	if err := discoveredwork.Validate(&rec); err != nil {
		t.Fatalf("record rejected: %v", err)
	}
	if len(rec.CandidateTargetFiles) != 0 {
		t.Errorf("kept an escaping path: %v", rec.CandidateTargetFiles)
	}
}

// A push that shifts the line must not produce a second issue.
func TestReviewRecord_IDIgnoresTheLine(t *testing.T) {
	now := time.Now()
	a := reviewRecord(7, 100, ReviewFinding{Severity: "major", Pass: "security", Where: "a.go:1", Claim: "c"}, now)
	b := reviewRecord(7, 100, ReviewFinding{Severity: "major", Pass: "security", Where: "a.go:99", Claim: "c"}, now)
	if a.ID != b.ID {
		t.Fatalf("ids differ: %s %s", a.ID, b.ID)
	}
}
