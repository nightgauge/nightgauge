package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// #3424 AC 1: a PR pr-merge promoted to Closes ticks the boxes it met, closes
// the issue, and posts no partial-delivery comment.
func TestPostMergeClosingPRTicksMetBoxes(t *testing.T) {
	issue := "## Acceptance criteria\n\n- [ ] one code change\n- [ ] `integration-tests` passes against platform `main`.\n"
	pr := "## Acceptance criteria\n\n- ✅ met: one code change (this PR — diff: x.go)\n" +
		"- ✅ met: `integration-tests` passes against platform `main`. (this PR — pr-merge: check `integration-tests` concluded SUCCESS on PR #77)\n\n" +
		"Closes #600\n"
	res, _, _, commenter, editor := runPartialIssuePR(t, "OPEN", issue, pr, nil)
	if !res.IssueClosed || res.PartialDelivery || res.Reason == "partial_delivery" {
		t.Fatalf("res = %+v, want closed, not partial", res)
	}
	if commenter.calls != 0 {
		t.Errorf("partial-delivery comment posted %d time(s):\n%s", commenter.calls, commenter.body)
	}
	if !strings.Contains(editor.body, "- [x] one code change") || !strings.Contains(editor.body, "- [x] `integration-tests` passes") {
		t.Errorf("met boxes not ticked:\n%s", editor.body)
	}
	if len(res.TickedCriteria) != 2 {
		t.Errorf("TickedCriteria = %v, want two", res.TickedCriteria)
	}
}

// A failed tick on a closing merge is reported, not a failed close.
func TestPostMergeClosingPRTickFailureIsNotAFailedClose(t *testing.T) {
	issue := "## Acceptance criteria\n\n- [ ] one code change\n"
	pr := "## Acceptance criteria\n\n- ✅ met: one code change (this PR — diff: x.go)\n\nCloses #600\n"
	editor := &recordingBodyEditor{err: errors.New("edit refused")}
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", Body: issue},
	}}
	res := EvaluatePostMerge(context.Background(), fetcher, &mockIssueCloser{},
		&mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped", Reason: "has_open"}},
		&mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: pr},
		&mockBoardSyncer{}, PostMergeInput{IssueNumber: 600, RepositoryOwner: "nightgauge", RepositoryName: "nightgauge",
			ProjectNumber: 6, PRNumber: 77, BodyEditor: editor})
	if !res.IssueClosed || res.TickedCriteria != nil || editor.calls != 1 {
		t.Errorf("IssueClosed=%v Ticked=%v calls=%d", res.IssueClosed, res.TickedCriteria, editor.calls)
	}
	if res.Failed && strings.Contains(res.Error, "edit refused") {
		t.Errorf("tick failure failed the close: %+v", res)
	}
}
