package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// recordingCommenter implements IssueCommenter.
type recordingCommenter struct {
	subject string
	body    string
	calls   int
	err     error
}

func (c *recordingCommenter) AddComment(_ context.Context, subjectID, body string) error {
	c.calls++
	c.subject, c.body = subjectID, body
	return c.err
}

const partialBody = "## Summary\n\nImplements #600: thing\n\n" +
	"## Acceptance criteria\n\n- ✅ met: first\n- ❌ unmet: second\n- ❌ unmet: third\n\n" +
	"## ⚠️ Unmet acceptance criteria\n\nThis PR leaves #600 open. These criteria remain:\n\n" +
	"- second\n- third\n\nPart of #12\nRefs #600\n"

func runPartial(t *testing.T, body string, prNumber int) (PostMergeResult, *mockIssueCloser, *mockEpicAutoCloser, *mockBoardSyncer, *recordingCommenter) {
	t.Helper()
	fetcher := &mockFetcher{issues: map[string]*types.Issue{
		"nightgauge/nightgauge#600": {NodeID: "I_node600", Number: 600, State: "OPEN", ParentIssueNumber: 12},
	}}
	closer := &mockIssueCloser{}
	epic := &mockEpicAutoCloser{result: &gh.AutoCloseSingleResult{Status: "skipped", Reason: "has_open"}}
	board := &mockBoardSyncer{}
	commenter := &recordingCommenter{}
	verifier := &mockPRVerifierWithMerge{state: "MERGED", sha: "abc", mergedAt: "2026-10-26T00:00:00Z", body: body}
	res := EvaluatePostMerge(context.Background(), fetcher, closer, epic, verifier, board, PostMergeInput{
		IssueNumber:     600,
		RepositoryOwner: "nightgauge",
		RepositoryName:  "nightgauge",
		ProjectNumber:   6,
		PRNumber:        prNumber,
		Remainder:       commenter,
	})
	return res, closer, epic, board, commenter
}

func TestPostMergeRefsBodyLeavesIssueOpenAndReturnsToReady(t *testing.T) {
	res, closer, epic, board, commenter := runPartial(t, partialBody, 77)

	if closer.called {
		t.Error("CloseIssue must not run for a PR that says Refs")
	}
	if res.IssueClosed || res.SurvivalEligible {
		t.Errorf("IssueClosed=%v SurvivalEligible=%v, want both false", res.IssueClosed, res.SurvivalEligible)
	}
	if res.Reason != "partial_delivery" || !res.PartialDelivery {
		t.Errorf("Reason=%q PartialDelivery=%v, want partial_delivery/true", res.Reason, res.PartialDelivery)
	}
	if board.calledWith.status != "Ready" || board.syncs != 1 {
		t.Errorf("board synced %d time(s), last to %q; want once to Ready", board.syncs, board.calledWith.status)
	}
	if epic.called {
		t.Error("epic rollup must not count a partial delivery")
	}
	if got := strings.Join(res.UnmetCriteria, "|"); got != "second|third" {
		t.Errorf("UnmetCriteria = %q, want second|third", got)
	}
	if commenter.calls != 1 || commenter.subject != "I_node600" {
		t.Fatalf("comment calls=%d subject=%q", commenter.calls, commenter.subject)
	}
	for _, want := range []string{"PR #77", "- second", "- third", "Ready"} {
		if !strings.Contains(commenter.body, want) {
			t.Errorf("comment missing %q:\n%s", want, commenter.body)
		}
	}
	if res.RemainderComment != "posted" || res.Failed {
		t.Errorf("RemainderComment=%q Failed=%v", res.RemainderComment, res.Failed)
	}
	if res.ValidationMarker != ValidationMarkerNotClosing {
		t.Errorf("ValidationMarker = %q, want %q", res.ValidationMarker, ValidationMarkerNotClosing)
	}
}

func TestPostMergeClosesBodyKeepsClosing(t *testing.T) {
	body := strings.Replace(partialBody, "Refs #600", "Closes #600", 1)
	res, closer, _, board, commenter := runPartial(t, body, 77)
	if !closer.called || !res.IssueClosed {
		t.Error("a Closes body must close the issue")
	}
	if board.calledWith.status != "Done" {
		t.Errorf("board status = %q, want Done", board.calledWith.status)
	}
	if commenter.calls != 0 || res.PartialDelivery {
		t.Error("a Closes body is not a partial delivery")
	}
}

func TestPostMergeUnreadBodyKeepsClosing(t *testing.T) {
	// An empty body means the merge info carried none; the hook cannot tell a
	// partial delivery, so it keeps today's close.
	res, closer, _, _, _ := runPartial(t, "", 77)
	if !closer.called || !res.IssueClosed || res.PartialDelivery {
		t.Errorf("closer.called=%v IssueClosed=%v PartialDelivery=%v", closer.called, res.IssueClosed, res.PartialDelivery)
	}
}

func TestPostMergeZeroPRNumberKeepsClosing(t *testing.T) {
	res, closer, _, _, _ := runPartial(t, partialBody, 0)
	if !closer.called || res.PartialDelivery {
		t.Error("without a PR number there is no body to read; the issue closes")
	}
}

func TestClosesIssue(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"Closes #600", true},
		{"closed #600", true},
		{"Fixes: #600", true},
		{"resolves nightgauge/nightgauge#600", true},
		{"Refs #600", false},
		{"Closes #6000", false},
		{"Closes #60", false},
		{"Implements #600\nPart of #600", false},
		{"Refs #600\nCloses #12", false},
	}
	for _, c := range cases {
		if got := ClosesIssue(c.body, 600); got != c.want {
			t.Errorf("ClosesIssue(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestUnmetCriteriaStopsAtNextHeading(t *testing.T) {
	body := unmetCriteriaHeading + "\n\n- a\n- b\n\n## Knowledge\n\n- not this\n"
	if got := strings.Join(UnmetCriteria(body), "|"); got != "a|b" {
		t.Errorf("UnmetCriteria = %q, want a|b", got)
	}
}

// RenderBody (#2655) writes each open criterion once, with its status on an
// indented line. That line must not be read as a criterion.
func TestUnmetCriteriaIgnoresIndentedStatusLine(t *testing.T) {
	body := unmetCriteriaHeading + "\n\nThis PR leaves #7 open.\n\n" +
		"- a\n  _❔ not verifiable — validate: no evidence_\n" +
		"- b\n  _❌ unmet — validate: not met_\n\nRefs #7\n"
	if got := strings.Join(UnmetCriteria(body), "|"); got != "a|b" {
		t.Errorf("UnmetCriteria = %q, want a|b", got)
	}
}

func TestRemainderCommentCannotCarryValidationMarker(t *testing.T) {
	body := remainderComment(7, []string{"x " + forge.ValidationMarkerComment("main", "abc")})
	if forge.HasValidationMarker(body) {
		t.Fatalf("remainder comment carries a validation marker: %q", body)
	}
}
