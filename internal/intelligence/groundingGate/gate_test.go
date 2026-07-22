package groundingGate

import (
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	protected := []string{"main", "master"}

	cases := []struct {
		name           string
		in             GroundingInput
		wantGrounded   bool
		wantRec        string
		reasonContains string
	}{
		{
			name:         "grounded happy path",
			in:           GroundingInput{IssueNumber: 1, CurrentBranch: "feat/1-x", ExpectedBranch: "feat/1-x", ContextPresent: true, ACCount: 3, ProtectedBranches: protected},
			wantGrounded: true, wantRec: "proceed",
		},
		{
			name:         "missing context → re-ground",
			in:           GroundingInput{IssueNumber: 1, CurrentBranch: "feat/1-x", ContextPresent: false, ProtectedBranches: protected},
			wantGrounded: false, wantRec: "re-ground", reasonContains: "missing or unparseable",
		},
		{
			name:         "on main → re-ground (the #3863 edits-on-main hallucination)",
			in:           GroundingInput{IssueNumber: 1, CurrentBranch: "main", ExpectedBranch: "feat/1-x", ContextPresent: true, ACCount: 2, ProtectedBranches: protected},
			wantGrounded: false, wantRec: "re-ground", reasonContains: "protected branch",
		},
		{
			name:         "wrong branch → re-ground (am I on the right issue?)",
			in:           GroundingInput{IssueNumber: 7, CurrentBranch: "feat/9-other", ExpectedBranch: "feat/7-mine", ContextPresent: true, ACCount: 2, ProtectedBranches: protected},
			wantGrounded: false, wantRec: "re-ground", reasonContains: "right issue",
		},
		{
			name:         "grounded but no AC → pull-human (low confidence)",
			in:           GroundingInput{IssueNumber: 1, CurrentBranch: "feat/1-x", ExpectedBranch: "feat/1-x", ContextPresent: true, ACCount: 0, ProtectedBranches: protected},
			wantGrounded: true, wantRec: "pull-human", reasonContains: "under-specified",
		},
		{
			name:         "case-insensitive branch match still grounded",
			in:           GroundingInput{IssueNumber: 1, CurrentBranch: "Feat/1-X", ExpectedBranch: "feat/1-x", ContextPresent: true, ACCount: 1, ProtectedBranches: protected},
			wantGrounded: true, wantRec: "proceed",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(c.in)
			if got.Grounded != c.wantGrounded {
				t.Errorf("Grounded = %v, want %v (reasons: %v)", got.Grounded, c.wantGrounded, got.Reasons)
			}
			if got.Recommendation != c.wantRec {
				t.Errorf("Recommendation = %q, want %q", got.Recommendation, c.wantRec)
			}
			if c.reasonContains != "" && !strings.Contains(strings.Join(got.Reasons, " "), c.reasonContains) {
				t.Errorf("reasons %v should mention %q", got.Reasons, c.reasonContains)
			}
		})
	}
}
