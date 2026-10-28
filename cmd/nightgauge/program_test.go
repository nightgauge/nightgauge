package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/internal/workorder"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

type fakeLabeledLister struct {
	issues map[string][]pkgtypes.Issue // "owner/repo label" -> issues
	calls  []string
	fail   string
}

func (f *fakeLabeledLister) ListIssuesByLabel(_ context.Context, owner, repo, label string) ([]pkgtypes.Issue, error) {
	key := owner + "/" + repo + " " + label
	f.calls = append(f.calls, key)
	if key == f.fail {
		return nil, errors.New("boom")
	}
	return f.issues[key], nil
}

func TestProgramCmd_HasStatus(t *testing.T) {
	cmd := programCmd()
	sub, _, err := cmd.Find([]string{"status"})
	if err != nil || sub.Name() != "status" {
		t.Fatalf("program status not registered: %v", err)
	}
	if sub.Flags().Lookup("programs") == nil || sub.Flags().Lookup("id") == nil || sub.Flags().Lookup("json") == nil {
		t.Fatal("program status is missing --programs, --id or --json")
	}
}

// Each program's selector is listed only in the repositories it covers, and
// an unreadable repository fails the report instead of undercounting it.
func TestListProgramIssues_ReadsEachProgramInItsRepos(t *testing.T) {
	f, err := workorder.Parse([]byte(`programs:
  - { id: billing, rank: 1, repos: [api] }
  - { id: docs, rank: 2 }
`))
	if err != nil {
		t.Fatal(err)
	}
	lister := &fakeLabeledLister{issues: map[string][]pkgtypes.Issue{
		"o/api program:billing": {{Repo: "o/api", Number: 1, State: "OPEN", Labels: []string{"program:billing"}}},
	}}
	got, err := listProgramIssues(context.Background(), lister, f, []string{"o/api", "o/web"})
	if err != nil {
		t.Fatalf("listProgramIssues: %v", err)
	}
	if strings.Join(lister.calls, ",") != "o/api program:billing,o/api program:docs,o/web program:docs" {
		t.Fatalf("calls = %v", lister.calls)
	}
	if len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("issues = %+v", got)
	}

	lister.fail = "o/web program:docs"
	if _, err := listProgramIssues(context.Background(), lister, f, []string{"o/api", "o/web"}); err == nil {
		t.Fatal("an unreadable repository must fail the report")
	}
}

func TestKeepProgram(t *testing.T) {
	f, _ := workorder.Parse([]byte("programs: [{ id: a }, { id: b }]"))
	if err := keepProgram(f, "b"); err != nil || len(f.Programs) != 1 || f.Programs[0].ID != "b" {
		t.Fatalf("keepProgram = %v, programs %+v", err, f.Programs)
	}
	if err := keepProgram(f, "zzz"); err == nil {
		t.Fatal("an unknown program id must be an error")
	}
}

func TestPrintProgramStatus(t *testing.T) {
	var buf bytes.Buffer
	printProgramStatus(&buf, []workorder.ProgramStatus{{
		ID: "billing", Title: "Customers can pay", Selector: "program:billing",
		Total: 4, Open: 2, Closed: 2, Validated: 1, Dispatchable: 1,
		DispatchableRefs: []string{"o/api#1"},
		Blocked:          []workorder.Item{{Ref: "o/api#2", Unresolved: []string{"o/core#9"}}},
	}})
	out := buf.String()
	for _, want := range []string{
		"1. billing — Customers can pay  [program:billing]",
		"4 issues: 2 open, 2 closed, 1 validated, 1 dispatchable",
		"ready    o/api#1",
		"blocked  o/api#2  (o/core#9 (state unresolved))",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// fakeMarkerComments answers TrustedComments by "owner/repo#N".
type fakeMarkerComments struct {
	byKey map[string][]trust.Comment
	err   error
	reads []string
}

func (f *fakeMarkerComments) TrustedComments(_ context.Context, owner, repo string, number int) ([]trust.Comment, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	f.reads = append(f.reads, key)
	return f.byKey[key], f.err
}

// fakeClosingMerges answers LatestClosingMerge by "owner/repo#N".
type fakeClosingMerges struct {
	byKey map[string]*gh.ClosingMerge
	err   error
}

func (f *fakeClosingMerges) LatestClosingMerge(_ context.Context, owner, repo string, number int) (*gh.ClosingMerge, error) {
	return f.byKey[fmt.Sprintf("%s/%s#%d", owner, repo, number)], f.err
}

// Validated reads the marker from closed issues only, once each, and a failed
// read fails the report rather than undercounting (#2574).
func TestReadValidationMarks(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Repo: "o/api", Number: 1, State: "CLOSED"},
		{Repo: "o/api", Number: 1, State: "CLOSED"},
		{Repo: "o/api", Number: 2, State: "CLOSED"},
		{Repo: "o/api", Number: 3, State: "OPEN"},
	}
	src := &fakeMarkerComments{byKey: map[string][]trust.Comment{
		"o/api#1": {{Body: forge.ValidationMarkerComment("main", "abc")}},
		"o/api#2": {{Body: "looks good"}},
	}}
	marked, err := readValidationMarks(context.Background(), src, &fakeClosingMerges{}, issues)
	if err != nil {
		t.Fatal(err)
	}
	if !marked[workorder.IssueKey("o/api", 1)] || marked[workorder.IssueKey("o/api", 2)] || len(marked) != 1 {
		t.Errorf("marked = %v, want only o/api#1", marked)
	}
	if strings.Join(src.reads, ",") != "o/api#1,o/api#2" {
		t.Errorf("reads = %v, want each closed issue once", src.reads)
	}
	if _, err := readValidationMarks(context.Background(), &fakeMarkerComments{err: errors.New("REST 502")}, &fakeClosingMerges{}, issues); err == nil {
		t.Error("a failed comment read did not fail the report")
	}
	if _, err := readValidationMarks(context.Background(), src, &fakeClosingMerges{err: errors.New("GraphQL 502")}, issues); err == nil {
		t.Error("a failed closing-merge read did not fail the report")
	}
	// (#2629) A marker older than a newer closing merge for another SHA is
	// stale: o/api#1 is not counted validated.
	stale := &fakeClosingMerges{byKey: map[string]*gh.ClosingMerge{
		"o/api#1": {Number: 9, SHA: "def", MergedAt: time.Now()},
	}}
	marked, err = readValidationMarks(context.Background(), src, stale, issues)
	if err != nil || len(marked) != 0 {
		t.Errorf("stale marker: marked = %v, err = %v; want none", marked, err)
	}
}
