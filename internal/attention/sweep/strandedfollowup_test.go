package sweep

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// strandedFake is an in-memory repo for StrandedFollowUpPRs: open PRs, issue
// states, and the tracking issues the producer files.
type strandedFake struct {
	prs      []forge.OpenPRSummary
	issues   map[int]string // number -> state
	filed    []forgetypes.Issue
	boarded  []int
	boardErr error
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
		// Like the real search: open issues only, matched on title — search
		// results carry no body.
		FindTracking: func(_ context.Context, _ Input, title string) (*forgetypes.Issue, error) {
			for i := range f.filed {
				if f.filed[i].Title == title && f.issues[f.filed[i].Number] != "CLOSED" {
					return &forgetypes.Issue{Number: f.filed[i].Number, Title: title, State: "OPEN"}, nil
				}
			}
			return nil, nil
		},
		// Like the real CreateIssue: the returned issue reports no state.
		FileTracking: func(_ context.Context, _ Input, title, body string) (*forgetypes.Issue, error) {
			is := forgetypes.Issue{Number: 9000 + len(f.filed), Title: title, Body: body}
			f.filed = append(f.filed, is)
			if f.issues == nil {
				f.issues = map[int]string{}
			}
			f.issues[is.Number] = "OPEN"
			return &is, nil
		},
		AddToBoard: func(_ context.Context, _ Input, number int) error {
			if f.boardErr != nil {
				return f.boardErr
			}
			f.boarded = append(f.boarded, number)
			return nil
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

// cardsAsExisting turns a pass's requests into the open cards the next pass
// sees, as the sweeper does.
func cardsAsExisting(reqs []attention.DecisionRequest) []attention.DecisionRequest {
	out := make([]attention.DecisionRequest, len(reqs))
	for i, r := range reqs {
		r.Producer = ProducerStrandedFollowUpPRs
		out[i] = r
	}
	return out
}

func TestStrandedFollowUpPRs_CardNamesPRAndTrackingIssue(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues: map[int]string{2655: "CLOSED"},
	}
	reqs, err := f.producer().Evaluate(context.Background(), in)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("reqs = %+v, err = %v", reqs, err)
	}
	if c := reqs[0].Context; c.PR != 2662 || c.Issue != 9000 {
		t.Errorf("context = %+v, want PR 2662 and issue 9000", c)
	}
	if len(f.boarded) != 1 || f.boarded[0] != 9000 {
		t.Errorf("boarded = %v, want [9000]", f.boarded)
	}
}

func TestStrandedFollowUpPRs_ClosedTrackingIssueIsNotRefiled(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues: map[int]string{2655: "CLOSED"},
	}
	p := f.producer()
	reqs, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// The tracking issue closes while the stranded PR stays open: the search
	// cannot see it, but the card's fingerprint still names it.
	f.issues[9000] = "CLOSED"
	in.Existing = cardsAsExisting(reqs)
	f.boarded = nil
	reqs, err = p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.filed) != 1 {
		t.Fatalf("filed %d tracking issues, want 1", len(f.filed))
	}
	if len(reqs) != 1 || reqs[0].Context.Issue != 9000 {
		t.Fatalf("reqs = %+v", reqs)
	}
	if len(f.boarded) != 0 {
		t.Errorf("a closed tracking issue must not be boarded: %v", f.boarded)
	}
}

func TestStrandedFollowUpPRs_BoardFailureIsRetried(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:      []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues:   map[int]string{2655: "CLOSED"},
		boardErr: errors.New("board unavailable"),
	}
	p := f.producer()
	if _, err := p.Evaluate(context.Background(), in); err == nil {
		t.Fatal("a board failure must be an error")
	}
	f.boardErr = nil
	if _, err := p.Evaluate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(f.filed) != 1 || len(f.boarded) != 1 || f.boarded[0] != 9000 {
		t.Fatalf("filed = %d, boarded = %v: the next pass must board the issue it already filed", len(f.filed), f.boarded)
	}
}

// strandedForge is the forge the real filing path uses: GraphQL for the
// repository id, Issues() for labels and CreateIssue. It has no GraphQL
// surface; strandedGQLForge adds one.
type strandedForge struct {
	forge.ForgeClient
	issues *strandedIssues
}

func (f *strandedForge) Issues() forge.IssueService { return f.issues }

type strandedGQLForge struct {
	strandedForge
	queries int
}

func (f *strandedGQLForge) ExecuteGraphQL(context.Context, string, map[string]interface{}) ([]byte, error) {
	f.queries++
	return []byte(`{"data":{"repository":{"id":"R_repo"}}}`), nil
}

type strandedIssues struct {
	forge.IssueService
	createdIn []string
}

func (s *strandedIssues) GetRepoLabels(context.Context, string, string) (map[string]string, error) {
	return map[string]string{"bug": "L_bug"}, nil
}

func (s *strandedIssues) CreateIssue(_ context.Context, repoID, title, body string, _ []string) (*forgetypes.Issue, error) {
	s.createdIn = append(s.createdIn, repoID)
	return &forgetypes.Issue{Number: 9100, Title: title, Body: body}, nil
}

// TestStrandedFollowUpPRs_FilesThroughSharedReadsWrap runs the producer's own
// filing path through the client the sweep passes it, NewSharedReads().Wrap,
// which embeds only forge.ForgeClient (#2684).
func TestStrandedFollowUpPRs_FilesThroughSharedReadsWrap(t *testing.T) {
	newProducer := func() *StrandedFollowUpPRs {
		f := &strandedFake{
			prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
			issues: map[int]string{2655: "CLOSED"},
		}
		p := f.producer()
		p.FileTracking = nil // the real path: repository id, labels, CreateIssue
		return p
	}

	t.Run("inner client with GraphQL files the tracking issue", func(t *testing.T) {
		inner := &strandedGQLForge{strandedForge: strandedForge{issues: &strandedIssues{}}}
		in := Input{Repo: "o/r", Owner: "o", Name: "r", Forge: NewSharedReads().Wrap(inner)}
		reqs, err := newProducer().Evaluate(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if inner.queries != 1 {
			t.Errorf("GraphQL queries through the wrapper = %d, want 1", inner.queries)
		}
		if got := inner.issues.createdIn; len(got) != 1 || got[0] != "R_repo" {
			t.Fatalf("CreateIssue repo ids = %v, want [R_repo]", got)
		}
		if len(reqs) != 1 || !strings.Contains(reqs[0].Body, "#9100") {
			t.Fatalf("card must name the filed tracking issue: %+v", reqs)
		}
	})

	t.Run("inner client without GraphQL files nothing", func(t *testing.T) {
		inner := &strandedForge{issues: &strandedIssues{}}
		in := Input{Repo: "o/r", Owner: "o", Name: "r", Forge: NewSharedReads().Wrap(inner)}
		_, err := newProducer().Evaluate(context.Background(), in)
		if !errors.Is(err, forge.ErrUnsupported) {
			t.Fatalf("err = %v, want one wrapping forge.ErrUnsupported", err)
		}
		if len(inner.issues.createdIn) != 0 {
			t.Fatalf("filed %v with no GraphQL surface", inner.issues.createdIn)
		}
	})
}
