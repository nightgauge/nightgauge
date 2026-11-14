package main

import (
	"context"
	"errors"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

type eligForge struct {
	levels    map[string]trust.Level
	open      []gh.OpenIssue
	permCalls int
}

func (f *eligForge) RepoPermission(_ context.Context, _, _, login string) (trust.Level, error) {
	f.permCalls++
	if l, ok := f.levels[login]; ok {
		return l, nil
	}
	return "", errors.New("REST 404")
}

func (f *eligForge) IssueSignals(context.Context, string, string, int) (trust.Signals, error) {
	return trust.Signals{}, errors.New("not read")
}

func (f *eligForge) OpenIssues(context.Context, string, string) ([]gh.OpenIssue, error) {
	return f.open, nil
}

// The report decides each open issue: a writer's and the pipeline App's are
// eligible; github-actions[bot]'s are not unless trusted_bots lists it; and
// the report costs one permission call per distinct human author (#2130).
func TestEligibilityReport_TodaysAuthorsStayEligible(t *testing.T) {
	f := &eligForge{levels: map[string]trust.Level{"maintainer": trust.LevelAdmin, "outsider": trust.LevelRead}}
	for i := 1; i <= 40; i++ {
		f.open = append(f.open, gh.OpenIssue{Number: i, Author: "maintainer"})
	}
	f.open = append(f.open,
		gh.OpenIssue{Number: 41, Author: "nightgauge-pipeline[bot]"},
		gh.OpenIssue{Number: 42, Author: "github-actions[bot]"},
		gh.OpenIssue{Number: 43, Author: "outsider", Labels: []string{"type:bug"}})
	rep, err := buildEligibilityReport(context.Background(), trust.NewGate(f, nil), f, readyBoard(nil), "nightgauge", "nightgauge", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	// github-actions[bot] is no longer trusted by default (#2130).
	if rep.Total != 43 || rep.Eligible != 41 || rep.Ineligible != 2 {
		t.Fatalf("report = %d total, %d eligible, %d not; want 43/41/2", rep.Total, rep.Eligible, rep.Ineligible)
	}
	if last := rep.Issues[42]; last.Eligible || last.AuthorLevel != "read" {
		t.Errorf("outsider row = %+v", last)
	}
	if f.permCalls != 2 {
		t.Errorf("permission calls = %d, want 2 (one per distinct human author)", f.permCalls)
	}
}

// fakePlacements serves board placements by issue number; an unknown number is
// off the board.
type fakePlacements struct {
	byNumber map[int]gh.BoardPlacement
	calls    int
}

func (f *fakePlacements) Placement(_ context.Context, _, _ string, number, _ int) (gh.BoardPlacement, error) {
	f.calls++
	if p, ok := f.byNumber[number]; ok {
		return p, nil
	}
	if f.byNumber == nil {
		return gh.BoardPlacement{OnBoard: true, Status: "Ready", Labels: []string{gh.LabelRefined}}, nil
	}
	return gh.BoardPlacement{}, nil
}

// readyBoard returns a checker on board 1; nil placements put every issue in
// Ready, refined.
func readyBoard(byNumber map[int]gh.BoardPlacement) readinessChecker {
	return newReadinessChecker(nil, &fakePlacements{byNumber: byNumber}, 1)
}

// A trusted issue the scheduler would skip is NOT eligible and names the skip
// reason; a trusted Ready issue stays eligible (#3278).
func TestEligibilityReport_BoardReadiness(t *testing.T) {
	f := &eligForge{levels: map[string]trust.Level{"maintainer": trust.LevelAdmin}}
	f.open = []gh.OpenIssue{
		{Number: 1, Author: "maintainer"},
		{Number: 2, Author: "maintainer"},
		{Number: 3, Author: "maintainer"},
	}
	board := readyBoard(map[int]gh.BoardPlacement{
		1: {OnBoard: true, Status: "Ready"},
		// 2 is off the board.
		3: {OnBoard: true, Status: "Ready", Labels: []string{"owner-action"}},
	})
	rep, err := buildEligibilityReport(context.Background(), trust.NewGate(f, nil), f, board, "acme", "app", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Eligible != 1 || rep.Ineligible != 2 {
		t.Fatalf("eligible/ineligible = %d/%d, want 1/2: %+v", rep.Eligible, rep.Ineligible, rep.Issues)
	}
	if r := rep.Issues[0]; !r.Eligible || !r.OnBoard || r.BoardStatus != "Ready" || len(r.SkipReasons) != 0 {
		t.Errorf("Ready row = %+v", r)
	}
	off := rep.Issues[1]
	if off.Eligible || !off.Trusted || off.OnBoard || len(off.SkipReasons) == 0 || off.SkipReasons[0].Code != "not-on-board" {
		t.Errorf("off-board row = %+v, want NOT eligible naming not-on-board", off)
	}
	if ex := rep.Issues[2]; ex.Eligible || len(ex.SkipReasons) != 1 || ex.SkipReasons[0].Code != "excluded-label" {
		t.Errorf("excluded row = %+v", ex)
	}
}

// With no board resolved for the repo, nothing is read and every issue is
// reported off the board — the scheduler dispatches only board items.
func TestEligibilityReport_NoBoard(t *testing.T) {
	f := &eligForge{levels: map[string]trust.Level{"maintainer": trust.LevelAdmin}}
	f.open = []gh.OpenIssue{{Number: 1, Author: "maintainer"}}
	reader := &fakePlacements{}
	rep, err := buildEligibilityReport(context.Background(), trust.NewGate(f, nil), f, newReadinessChecker(nil, reader, 0), "acme", "app", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 0 || rep.Eligible != 0 || rep.Issues[0].SkipReasons[0].Code != "not-on-board" {
		t.Fatalf("calls=%d report=%+v", reader.calls, rep)
	}
}
