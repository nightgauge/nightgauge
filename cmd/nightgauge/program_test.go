package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
