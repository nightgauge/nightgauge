package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// assessFetcher serves issues to epicAssessInputs the way
// github.IssueService.GetIssueWithRelations does: the relationship lists a
// read does not name come back empty.
type assessFetcher struct {
	issues map[int]*types.Issue
	// truncated names, per issue, the connections whose later pages cannot
	// be read: a read naming any of them fails with ErrConnectionTruncated.
	truncated map[int]gh.IssueRelations
	// failed makes every read of the issue fail with the given error.
	failed map[int]error
	// reads records the repository and number of every read, "owner/repo#N".
	reads []string
}

func (f *assessFetcher) GetIssueWithRelations(_ context.Context, owner, repo string, number int, rels gh.IssueRelations) (*types.Issue, error) {
	f.reads = append(f.reads, fmt.Sprintf("%s/%s#%d", owner, repo, number))
	if f.truncated[number]&rels != 0 {
		return nil, fmt.Errorf("fetch issue #%d: relationship of acme/widgets#%d: %w: read page 2: status 502",
			number, number, forge.ErrConnectionTruncated)
	}
	if err := f.failed[number]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[number]
	if !ok {
		return nil, fmt.Errorf("issue #%d not found", number)
	}
	out := *issue
	if rels&gh.RelationSubIssues == 0 {
		out.SubIssues = nil
	}
	if rels&gh.RelationBlockedBy == 0 {
		out.BlockedBy = nil
	}
	if rels&gh.RelationBlocking == 0 {
		out.Blocking = nil
	}
	return &out, nil
}

// assessEpic is epic #100 with open sub-issues #101 and #102, where #102 is
// blocked by #101.
func assessEpic() *assessFetcher {
	return &assessFetcher{issues: map[int]*types.Issue{
		100: {Number: 100, Title: "Epic", SubIssues: []types.SubIssueRef{
			{Number: 101, State: "OPEN"},
			{Number: 102, State: "OPEN"},
			{Number: 103, State: "CLOSED"},
		}},
		101: {Number: 101, Title: "First"},
		102: {Number: 102, Title: "Second", BlockedBy: []types.BlockingRef{{Number: 101, State: "OPEN"}}},
	}}
}

// TestEpicAssessInputs_TruncatedBlockerListIsAnError: #102's blocker list
// cannot be read to its end. Skipping #102 with a warning computed the
// strategy without it and without the blockers that order it, and the
// assess-epic skill discards the warning, so the assessment must fail.
func TestEpicAssessInputs_TruncatedBlockerListIsAnError(t *testing.T) {
	f := assessEpic()
	f.truncated = map[int]gh.IssueRelations{102: gh.RelationBlockedBy}

	inputs, err := epicAssessInputs(context.Background(), f, "acme", "widgets", 100)
	if !errors.Is(err, forge.ErrConnectionTruncated) {
		t.Fatalf("err = %v (inputs %+v), want ErrConnectionTruncated", err, inputs)
	}
	if inputs != nil {
		t.Errorf("inputs = %+v, want none alongside a truncated read", inputs)
	}
}

// TestEpicAssessInputs_ReadsOnlyTheListsItUses: the assessment uses the
// epic's sub-issue list and each sub-issue's blockers. A long list of any
// other kind that cannot be read whole must not fail it or drop a sub-issue.
func TestEpicAssessInputs_ReadsOnlyTheListsItUses(t *testing.T) {
	f := assessEpic()
	f.truncated = map[int]gh.IssueRelations{
		100: gh.RelationBlockedBy | gh.RelationBlocking,
		101: gh.RelationSubIssues | gh.RelationBlocking,
		102: gh.RelationSubIssues | gh.RelationBlocking,
	}

	inputs, err := epicAssessInputs(context.Background(), f, "acme", "widgets", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 2 || inputs[0].Number != 101 || inputs[1].Number != 102 {
		t.Fatalf("inputs = %+v, want the open sub-issues #101 and #102", inputs)
	}
	if got := inputs[1].BlockedBy; len(got) != 1 || got[0] != 101 {
		t.Errorf("#102 BlockedBy = %v, want [101]", got)
	}
}

// TestEpicAssessInputs_UnreadableSubIssueIsAnError: #101 cannot be read at
// all. It was skipped with a warning the assess-epic skill discards, and the
// strategy was computed without it and without the edge that orders #102
// after it. A failed first read is now an error, as a truncated blocker list
// already was.
func TestEpicAssessInputs_UnreadableSubIssueIsAnError(t *testing.T) {
	f := assessEpic()
	readFailure := errors.New("status 502")
	f.failed = map[int]error{101: readFailure}

	inputs, err := epicAssessInputs(context.Background(), f, "acme", "widgets", 100)
	if !errors.Is(err, readFailure) || !strings.Contains(err.Error(), "acme/widgets#101") {
		t.Fatalf("err = %v (inputs %+v), want the read failure of acme/widgets#101", err, inputs)
	}
	if inputs != nil {
		t.Errorf("inputs = %+v, want none alongside a failed read", inputs)
	}
}

// TestEpicAssessInputs_ReadsEachSubIssueInItsOwnRepository: a sub-issue of a
// cross-repo epic was read by number in the epic's repository, which holds a
// different issue with that number, or none.
func TestEpicAssessInputs_ReadsEachSubIssueInItsOwnRepository(t *testing.T) {
	f := assessEpic()
	f.issues[100].SubIssues[1].Repo = "acme/gadgets"

	if _, err := epicAssessInputs(context.Background(), f, "acme", "widgets", 100); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[acme/widgets#100 acme/widgets#101 acme/gadgets#102]"
	if got := fmt.Sprint(f.reads); got != want {
		t.Errorf("reads = %s, want %s", got, want)
	}
}
