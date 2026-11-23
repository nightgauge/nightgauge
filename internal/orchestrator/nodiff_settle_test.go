package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/pkg/types"
)

type fakeNoDiffIssue struct {
	commentErr error
	closeErr   error
	comments   []string
	closes     int
}

func (f *fakeNoDiffIssue) AddComment(_ context.Context, _ string, body string) error {
	f.comments = append(f.comments, body)
	return f.commentErr
}

func (f *fakeNoDiffIssue) CloseIssue(_ context.Context, _ string) error {
	f.closes++
	return f.closeErr
}

// plainIssueBody reads a body whose Delivery order does not say refs.
func plainIssueBody(context.Context) (string, error) {
	return "## Delivery order\n\n1. One PR closes it.\n", nil
}

func noDiffResult(statuses ...string) pmstages.PRCreateResult {
	res := pmstages.PRCreateResult{Branch: "feat/3012-x", BaseBranch: "main"}
	for i, s := range statuses {
		res.ACVerdicts = append(res.ACVerdicts, pmstages.ACVerdict{Index: i + 1, Text: "criterion", Status: s})
	}
	return res
}

func TestSettleNoDiffAllMetClosesIssue(t *testing.T) {
	t.Parallel()

	f := &fakeNoDiffIssue{}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", plainIssueBody, 3012, noDiffResult(pmstages.ACMet, pmstages.ACMet))
	if !closed || f.closes != 1 || len(f.comments) != 1 {
		t.Fatalf("closed=%v closes=%d comments=%d, want closed with one comment", closed, f.closes, len(f.comments))
	}
	if !strings.HasPrefix(detail, "issue_closed:") {
		t.Errorf("detail = %q, want issue_closed", detail)
	}
}

func TestSettleNoDiffUnmetParksIssue(t *testing.T) {
	t.Parallel()

	f := &fakeNoDiffIssue{}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", plainIssueBody, 3012, noDiffResult(pmstages.ACMet, pmstages.ACUnmet))
	if closed || f.closes != 0 || len(f.comments) != 1 {
		t.Fatalf("closed=%v closes=%d comments=%d, want parked with one comment", closed, f.closes, len(f.comments))
	}
	if !strings.Contains(detail, "[not-pipeline-actionable]") || !strings.Contains(detail, "1 acceptance criteria") {
		t.Errorf("detail = %q, want not-pipeline-actionable naming 1 unmet", detail)
	}
}

func TestSettleNoDiffCommentFailurePreventsClose(t *testing.T) {
	t.Parallel()

	f := &fakeNoDiffIssue{commentErr: errors.New("boom")}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", plainIssueBody, 3012, noDiffResult(pmstages.ACMet))
	if closed || f.closes != 0 {
		t.Fatalf("closed=%v closes=%d, want no close after a failed comment", closed, f.closes)
	}
	if !strings.Contains(detail, "[not-pipeline-actionable]") {
		t.Errorf("detail = %q, want parked", detail)
	}
}

func TestSettleNoDiffCloseFailureParks(t *testing.T) {
	t.Parallel()

	f := &fakeNoDiffIssue{closeErr: errors.New("boom")}
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", plainIssueBody, 3012, noDiffResult(pmstages.ACMet))
	if closed || !strings.Contains(detail, "[not-pipeline-actionable]") {
		t.Errorf("closed=%v detail=%q, want parked after a failed close", closed, detail)
	}
}

func TestSettleNoDiffNoNodeIDRecordsNothing(t *testing.T) {
	t.Parallel()

	f := &fakeNoDiffIssue{}
	_, closed := SettleNoDiff(context.Background(), f, f, "", plainIssueBody, 3012, noDiffResult(pmstages.ACMet))
	if closed || len(f.comments) != 0 || f.closes != 0 {
		t.Errorf("closed=%v comments=%d closes=%d, want nothing recorded", closed, len(f.comments), f.closes)
	}
}

// #3510: with every criterion met, an issue whose Delivery order says refs,
// or whose body cannot be read, is not closed; the comment is still posted
// and says why. A plain body closes as before (TestSettleNoDiffAllMetClosesIssue).
func TestSettleNoDiffDeliveryOrderHold(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		read func(context.Context) (string, error)
		want string
	}{
		"refs": {func(context.Context) (string, error) {
			return "## Delivery order\n\n1. Step one. Each PR refs #3012.\n", nil
		}, "Delivery order says refs"},
		"read error": {func(context.Context) (string, error) { return "", errors.New("502") }, "could not be read"},
		"no reader":  {nil, "could not be read"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeNoDiffIssue{}
			detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", tc.read, 3012, noDiffResult(pmstages.ACMet, pmstages.ACMet))
			if closed || f.closes != 0 || len(f.comments) != 1 {
				t.Fatalf("closed=%v closes=%d comments=%d, want one comment and no close", closed, f.closes, len(f.comments))
			}
			if !strings.Contains(f.comments[0], tc.want) || !strings.Contains(f.comments[0], "stays open") ||
				strings.Contains(f.comments[0], pmstages.NoDiffClosedLine) {
				t.Errorf("comment does not say why the issue stays open:\n%s", f.comments[0])
			}
			if !strings.Contains(detail, "[not-pipeline-actionable]") || !strings.Contains(detail, tc.want) {
				t.Errorf("detail = %q, want not-pipeline-actionable naming the hold", detail)
			}
		})
	}
}

// #3510: the issue body is read only when every criterion is met.
func TestSettleNoDiffUnmetDoesNotReadBody(t *testing.T) {
	t.Parallel()
	calls := 0
	read := func(context.Context) (string, error) { calls++; return "", nil }
	f := &fakeNoDiffIssue{}
	if _, closed := SettleNoDiff(context.Background(), f, f, "I_1", read, 3012, noDiffResult(pmstages.ACMet, pmstages.ACUnmet)); closed || calls != 0 {
		t.Errorf("closed=%v reads=%d, want parked with no body read", closed, calls)
	}
}

// #3510: a held close stays held when the comment fails: no close, and the
// detail is the held one.
func TestSettleNoDiffDeliveryOrderHoldCommentFails(t *testing.T) {
	t.Parallel()
	refs := func(context.Context) (string, error) { return "## Delivery order\n\nrefs\n", nil }
	f := &fakeNoDiffIssue{commentErr: errors.New("boom")}
	res := noDiffResult(pmstages.ACMet)
	detail, closed := SettleNoDiff(context.Background(), f, f, "I_1", refs, 3012, res)
	if want := pmstages.NoDiffHeldDetail(3012, res.Branch, res.BaseBranch, holdPhrase(3012, HoldDeliveryOrderRefs)); closed || f.closes != 0 || detail != want {
		t.Errorf("closed=%v closes=%d detail=%q, want held detail %q", closed, f.closes, detail, want)
	}
}

type fakeBodyReader struct {
	iss *types.Issue
	err error
}

func (f fakeBodyReader) GetIssueWithRelations(context.Context, string, string, int, gh.IssueRelations) (*types.Issue, error) {
	return f.iss, f.err
}

// #3510: IssueBodyReader, which the scheduler and the pr-stage verb hand to
// SettleNoDiff, passes the body through, and a failed read, a nil issue or no
// reader holds the close.
func TestIssueBodyReaderSettles(t *testing.T) {
	t.Parallel()
	refsBody := "## Delivery order\n\nEach PR refs #3012.\n"
	for name, tc := range map[string]struct {
		r      PartialCloseIssueReader
		closed bool
	}{
		"refs body":  {fakeBodyReader{iss: &types.Issue{Body: refsBody}}, false},
		"read error": {fakeBodyReader{err: errors.New("502")}, false},
		"nil issue":  {fakeBodyReader{}, false},
		"no reader":  {nil, false},
		"plain body": {fakeBodyReader{iss: &types.Issue{Body: "## Delivery order\n\nOne PR closes it.\n"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := &fakeNoDiffIssue{}
			_, closed := SettleNoDiff(context.Background(), f, f, "I_1", IssueBodyReader(tc.r, "o", "r", 3012), 3012, noDiffResult(pmstages.ACMet))
			if closed != tc.closed || f.closes != map[bool]int{true: 1, false: 0}[tc.closed] || len(f.comments) != 1 {
				t.Errorf("closed=%v closes=%d comments=%d, want closed=%v with one comment", closed, f.closes, len(f.comments), tc.closed)
			}
		})
	}
}
