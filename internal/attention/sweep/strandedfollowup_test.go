package sweep

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/nightgauge/nightgauge/internal/forge/boardcache"
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
	planted  []forgetypes.Issue // open issues someone else filed
	selfErr  error
}

const strandedSelf = "nightgauge[bot]"

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
		// Like the real search: every open issue with the title, planted or
		// filed, with its body and author.
		SearchTracking: func(_ context.Context, _ Input, title string) ([]forgetypes.Issue, error) {
			var out []forgetypes.Issue
			for _, is := range append(append([]forgetypes.Issue{}, f.planted...), f.filed...) {
				if is.Title == title && f.issues[is.Number] != "CLOSED" {
					is.State = "OPEN"
					out = append(out, is)
				}
			}
			return out, nil
		},
		Self: func(context.Context, Input) (string, error) {
			if f.selfErr != nil {
				return "", f.selfErr
			}
			return strandedSelf, nil
		},
		// Like the real CreateIssue: the returned issue reports no state.
		FileTracking: func(_ context.Context, _ Input, title, body string) (*forgetypes.Issue, error) {
			is := forgetypes.Issue{Number: 9000 + len(f.filed), Title: title, Body: body, AuthorLogin: strandedSelf}
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
		{"body closes an open issue by URL", strandedPR(17, "feat/5-x", "Closes https://github.com/o/r/issues/7"),
			map[int]string{5: "CLOSED", 7: "OPEN"}, 0},
		{"another repository's open #7 lands nothing here", strandedPR(18, "feat/5-x",
			"Closes https://github.com/other/repo/issues/7\nFixes other/repo#7"),
			map[int]string{5: "CLOSED", 7: "OPEN"}, 1},
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

// Board and Project answer nil so boardcache.WrapClient can wrap the fake
// without a board behind it.
func (f *strandedForge) Board() forge.BoardService     { return nil }
func (f *strandedForge) Project() forge.ProjectService { return nil }

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
// filing path through the client the sweep passes it: NewSharedReads().Wrap
// over boardcache.WrapClient, both of which embed only forge.ForgeClient
// (#2684). The direct wrap is kept as a second composition.
func TestStrandedFollowUpPRs_FilesThroughSharedReadsWrap(t *testing.T) {
	newProducer := func() *StrandedFollowUpPRs {
		f := &strandedFake{
			prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
			issues: map[int]string{2655: "CLOSED"},
		}
		p := f.producer()
		p.FileTracking = nil // the real path: repository id, labels, CreateIssue
		p.SearchTracking = func(context.Context, Input, string) ([]forgetypes.Issue, error) { return nil, nil }
		return p
	}
	compositions := map[string]func(forge.ForgeClient) forge.ForgeClient{
		"sweep client (shared reads over board cache)": func(c forge.ForgeClient) forge.ForgeClient {
			return NewSharedReads().Wrap(boardcache.WrapClient(boardcache.New(0), c, "o", 1))
		},
		"shared reads only": func(c forge.ForgeClient) forge.ForgeClient {
			return NewSharedReads().Wrap(c)
		},
	}

	for name, wrap := range compositions {
		t.Run(name+": inner client with GraphQL files the tracking issue", func(t *testing.T) {
			inner := &strandedGQLForge{strandedForge: strandedForge{issues: &strandedIssues{}}}
			in := Input{Repo: "o/r", Owner: "o", Name: "r", Forge: wrap(inner)}
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

		t.Run(name+": inner client without GraphQL files nothing", func(t *testing.T) {
			inner := &strandedForge{issues: &strandedIssues{}}
			in := Input{Repo: "o/r", Owner: "o", Name: "r", Forge: wrap(inner)}
			_, err := newProducer().Evaluate(context.Background(), in)
			if !errors.Is(err, forge.ErrUnsupported) {
				t.Fatalf("err = %v, want one wrapping forge.ErrUnsupported", err)
			}
			if len(inner.issues.createdIn) != 0 {
				t.Fatalf("filed %v with no GraphQL surface", inner.issues.createdIn)
			}
		})
	}
}

// TestStrandedFollowUpPRs_IgnoresPlantedTrackingIssue: an open issue with the
// exact title and marker, filed by someone else, is not the tracking issue
// (#2688). The producer files and boards its own.
func TestStrandedFollowUpPRs_IgnoresPlantedTrackingIssue(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	pr := strandedPR(2662, "feat/2655-pr-create", "")
	title, body := strandedTrackingIssue(pr, 2655)
	f := &strandedFake{
		prs:     []forge.OpenPRSummary{pr},
		issues:  map[int]string{2655: "CLOSED", 666: "OPEN"},
		planted: []forgetypes.Issue{{Number: 666, Title: title, Body: body, AuthorLogin: "mallory"}},
	}
	reqs, err := f.producer().Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.filed) != 1 {
		t.Fatalf("filed %d tracking issues, want 1", len(f.filed))
	}
	if len(reqs) != 1 || reqs[0].Context.Issue != 9000 || strings.Contains(reqs[0].Body, "#666") {
		t.Fatalf("card must name the filed issue, not the planted one: %+v", reqs)
	}
	if len(f.boarded) != 1 || f.boarded[0] != 9000 {
		t.Errorf("boarded = %v, want [9000]", f.boarded)
	}
}

// TestStrandedFollowUpPRs_IdentityErrorFailsClosed: with no known identity
// nothing is filed or linked.
func TestStrandedFollowUpPRs_IdentityErrorFailsClosed(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:     []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues:  map[int]string{2655: "CLOSED"},
		selfErr: errors.New("whoami failed"),
	}
	if _, err := f.producer().Evaluate(context.Background(), in); err == nil {
		t.Fatal("an identity failure must be an error")
	}
	if len(f.filed) != 0 || len(f.boarded) != 0 {
		t.Fatalf("filed = %v, boarded = %v; want nothing", f.filed, f.boarded)
	}
}

// TestStrandedFollowUpPRs_AppDescriptionIdentityFailsClosed: a GitHub App with
// no slug reports a description, not a login; the pass must error rather than
// file a duplicate tracking issue on every run.
func TestStrandedFollowUpPRs_AppDescriptionIdentityFailsClosed(t *testing.T) {
	in := Input{Repo: "o/r", Owner: "o", Name: "r"}
	f := &strandedFake{
		prs:    []forge.OpenPRSummary{strandedPR(2662, "feat/2655-pr-create", "")},
		issues: map[int]string{2655: "CLOSED"},
	}
	p := f.producer()
	p.Self = func(context.Context, Input) (string, error) { return "github-app 1 (installation 2)", nil }
	if _, err := p.Evaluate(context.Background(), in); err == nil {
		t.Fatal("a non-login identity must be an error")
	}
	if len(f.filed) != 0 || len(f.boarded) != 0 {
		t.Fatalf("filed = %v, boarded = %v; want nothing", f.filed, f.boarded)
	}
}

func TestPickTracking(t *testing.T) {
	const title, marker = "t", "<!-- m -->"
	ok := forgetypes.Issue{Number: 1, Title: title, Body: "x " + marker, AuthorLogin: "Nightgauge[bot]"}
	cases := map[string]struct {
		hit  forgetypes.Issue
		self string
		want bool
	}{
		"own issue":      {ok, strandedSelf, true},
		"other author":   {func() forgetypes.Issue { h := ok; h.AuthorLogin = "mallory"; return h }(), strandedSelf, false},
		"unknown author": {func() forgetypes.Issue { h := ok; h.AuthorLogin = ""; return h }(), strandedSelf, false},
		"no marker":      {func() forgetypes.Issue { h := ok; h.Body = "x"; return h }(), strandedSelf, false},
		"other title":    {func() forgetypes.Issue { h := ok; h.Title = "t2"; return h }(), strandedSelf, false},
		"unknown self":   {ok, "", false},
	}
	for name, c := range cases {
		if got := pickTracking([]forgetypes.Issue{c.hit}, title, marker, c.self) != nil; got != c.want {
			t.Errorf("%s: picked = %v, want %v", name, got, c.want)
		}
	}
}
