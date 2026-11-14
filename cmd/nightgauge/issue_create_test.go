package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/pkg/types"
)

type fakeIssueCreator struct {
	known    map[string]bool
	created  []string // label IDs passed to Create
	boardAdd []string // "project:status"
	addErr   error
}

func (f *fakeIssueCreator) ResolveLabels(_ context.Context, _, _ string, names []string) ([]string, []string, error) {
	for _, n := range names {
		if !f.known[n] {
			return nil, nil, errors.New("unknown label " + n)
		}
	}
	return names, names, nil
}

func (f *fakeIssueCreator) Create(_ context.Context, _, _, title, _ string, ids []string) (*types.Issue, error) {
	f.created = append(f.created, ids...)
	return &types.Issue{Number: 50, Title: title}, nil
}

func (f *fakeIssueCreator) AddToBoard(_ context.Context, _, _ string, _, project int, status string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.boardAdd = append(f.boardAdd, fmt.Sprintf("%d:%s", project, status))
	return nil
}

func newFakeCreator() *fakeIssueCreator {
	return &fakeIssueCreator{known: map[string]bool{"type:bug": true, gh.LabelRefined: true, "owner-action": true}}
}

func codesOf(res issueCreateResult) string {
	var c []string
	for _, r := range res.DispatchSkipReasons {
		c = append(c, r.Code)
	}
	return strings.Join(c, ",")
}

func TestRunIssueCreate(t *testing.T) {
	backlogOn := true
	pickupOn := true
	cfgSweep := &config.Config{Autonomous: &config.AutonomousConfig{RefinementBacklog: &backlogOn, PickupBacklog: &pickupOn}}

	cases := []struct {
		name      string
		cfg       *config.Config
		opts      issueCreateOptions
		addErr    error
		wantBoard string
		wantCodes string
		wantLabel bool
	}{
		// Default: intake column, unrefined, scheduler defaults — Backlog is
		// not dispatched and the issue is never refined.
		{name: "intake default", opts: issueCreateOptions{Project: 3, Status: issueIntakeStatus},
			wantBoard: "3:Backlog", wantCodes: "board-status-not-dispatchable,backlog-refinement-disabled"},
		{name: "ready and refined", opts: issueCreateOptions{Project: 3, Status: "Ready", Refined: true},
			wantBoard: "3:Ready", wantCodes: "", wantLabel: true},
		{name: "off board", opts: issueCreateOptions{Project: 0, Status: "Ready", Refined: true},
			wantCodes: "not-on-board", wantLabel: true},
		{name: "board add fails", opts: issueCreateOptions{Project: 3, Status: "Ready", Refined: true}, addErr: errors.New("boom"),
			wantCodes: "not-on-board", wantLabel: true},
		{name: "excluded label", opts: issueCreateOptions{Project: 3, Status: "Ready", Labels: []string{"owner-action"}},
			wantBoard: "3:Ready", wantCodes: "excluded-label"},
		{name: "backlog with sweep and pickup on", cfg: cfgSweep, opts: issueCreateOptions{Project: 3, Status: issueIntakeStatus},
			wantBoard: "3:Backlog", wantCodes: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeCreator()
			c.addErr = tc.addErr
			tc.opts.Owner, tc.opts.Repo, tc.opts.Title = "acme", "app", "t"
			res, err := runIssueCreate(context.Background(), c, newReadinessChecker(tc.cfg, nil, tc.opts.Project), tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(c.boardAdd, ","); got != tc.wantBoard {
				t.Errorf("board add = %q, want %q", got, tc.wantBoard)
			}
			if got := codesOf(res); got != tc.wantCodes {
				t.Errorf("skip codes = %q, want %q", got, tc.wantCodes)
			}
			if got := hasLabelFold(c.created, gh.LabelRefined); got != tc.wantLabel {
				t.Errorf("refined label applied = %v, want %v", got, tc.wantLabel)
			}
		})
	}
}

// An unknown label fails before anything is created or placed.
func TestRunIssueCreate_UnknownLabelCreatesNothing(t *testing.T) {
	c := newFakeCreator()
	_, err := runIssueCreate(context.Background(), c, newReadinessChecker(nil, nil, 3),
		issueCreateOptions{Owner: "acme", Repo: "app", Title: "t", Labels: []string{"nope"}, Project: 3, Status: "Ready"})
	if err == nil || len(c.created) != 0 || len(c.boardAdd) != 0 {
		t.Fatalf("err=%v created=%v board=%v", err, c.created, c.boardAdd)
	}
}

// Each reason is printed with the command that clears it.
func TestPrintIssueCreateWarnings(t *testing.T) {
	c := newFakeCreator()
	c.addErr = errors.New("project not found")
	res, err := runIssueCreate(context.Background(), c, newReadinessChecker(nil, nil, 3),
		issueCreateOptions{Owner: "acme", Repo: "app", Title: "t", Project: 3, Status: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printIssueCreateWarnings(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"#50 was created but not added to board 3: project not found",
		"#50 will not be dispatched: not-on-board:",
		"fix: nightgauge project add 50 --repo acme/app --project 3 --status Ready",
		"#50 will not be dispatched: backlog-refinement-disabled:",
		"fix: nightgauge issue mark-refined 50 --repo acme/app",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("warnings missing %q:\n%s", want, out)
		}
	}
}
