package sweep

import (
	"context"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// strandedFake is an in-memory repo for StrandedFollowUpPRs: open PRs, issue
// states, and the tracking issues the producer files.
type strandedFake struct {
	prs    []forge.OpenPRSummary
	issues map[int]string // number -> state
	filed  []forgetypes.Issue
}

func (f *strandedFake) producer() *StrandedFollowUpPRs {
	return &StrandedFollowUpPRs{
		ListOpenPRs: func(context.Context, Input) ([]forge.OpenPRSummary, error) { return f.prs, nil },
		GetIssues: func(_ context.Context, _ Input, numbers []int) (map[int]*forgetypes.Issue, error) {
			out := map[int]*forgetypes.Issue{}
			for _, n := range numbers {
				if state, ok := f.issues[n]; ok {
					out[n] = &forgetypes.Issue{Number: n, State: state}
				}
			}
			return out, nil
		},
		FindTracking: func(_ context.Context, _ Input, pr int) (*forgetypes.Issue, error) {
			for i := range f.filed {
				if strings.Contains(f.filed[i].Body, StrandedFollowUpMarker(pr)) {
					return &f.filed[i], nil
				}
			}
			return nil, nil
		},
		FileTracking: func(_ context.Context, _ Input, title, body string) (*forgetypes.Issue, error) {
			is := forgetypes.Issue{Number: 9000 + len(f.filed), Title: title, Body: body, State: "OPEN"}
			f.filed = append(f.filed, is)
			return &is, nil
		},
	}
}

func strandedPR(n int, head, body string) forge.OpenPRSummary {
	return forge.OpenPRSummary{Number: n, HeadRef: head, HeadRepo: "o/r", Body: body}
}

func TestStrandedFollowUpPRs(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	cases := []struct {
		name      string
		pr        forge.OpenPRSummary
		issues    map[int]string
		wantCards int
	}{
		{"closed parent is stranded", strandedPR(2681, "fix/2672-ac-parse-fences-review", "Refs #2672"),
			map[int]string{2672: "CLOSED"}, 1},
		{"open parent is not stranded", strandedPR(10, "feat/5-x", ""), map[int]string{5: "OPEN"}, 0},
		{"body closes an open issue", strandedPR(11, "feat/5-x", "Closes #7"),
			map[int]string{5: "CLOSED", 7: "OPEN"}, 0},
		{"body closes a closed issue", strandedPR(12, "feat/5-x", "Fixes #5"), map[int]string{5: "CLOSED"}, 1},
		{"fork PR is never a candidate", func() forge.OpenPRSummary {
			pr := strandedPR(14, "feat/5-x", "")
			pr.HeadRepo = "attacker/r"
			return pr
		}(), map[int]string{5: "CLOSED"}, 0},
		{"head repo not reported", func() forge.OpenPRSummary {
			pr := strandedPR(15, "feat/5-x", "")
			pr.HeadRepo = ""
			return pr
		}(), map[int]string{5: "CLOSED"}, 0},
		{"unsafe ref characters", strandedPR(16, "feat/5-x`\ninjected", ""), map[int]string{5: "CLOSED"}, 0},
		{"branch without a number", strandedPR(13, "dependabot/npm/foo-1.2", ""), map[int]string{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &strandedFake{prs: []forge.OpenPRSummary{tc.pr}, issues: tc.issues}
			reqs, err := f.producer().Evaluate(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if len(reqs) != tc.wantCards || len(f.filed) != tc.wantCards {
				t.Fatalf("cards = %d, filed = %d, want %d", len(reqs), len(f.filed), tc.wantCards)
			}
		})
	}
}

func TestStrandedFollowUpPRs_FilesOnceAndResolves(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues: map[int]string{2655: "CLOSED"},
	}
	p := f.producer()
	for i := 0; i < 2; i++ {
		reqs, err := p.Evaluate(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if len(reqs) != 1 || reqs[0].IdempotencyKey != "pr-2662" {
			t.Fatalf("pass %d: reqs = %+v", i, reqs)
		}
		if !strings.Contains(reqs[0].Body, "#9000") {
			t.Errorf("card must name the tracking issue: %q", reqs[0].Body)
		}
	}
	if len(f.filed) != 1 {
		t.Fatalf("filed %d tracking issues, want 1", len(f.filed))
	}
	if got := f.filed[0].Title; got != "fix: land stranded follow-up PR #2662 (for #2655)" {
		t.Errorf("title = %q", got)
	}
	f.prs = nil // the PR merged or closed
	reqs, err := p.Evaluate(context.Background(), in)
	if err != nil || len(reqs) != 0 {
		t.Fatalf("resolved: reqs = %+v, err = %v", reqs, err)
	}
}
