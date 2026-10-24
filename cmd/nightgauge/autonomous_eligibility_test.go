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
	rep, err := buildEligibilityReport(context.Background(), trust.NewGate(f, nil), f, "nightgauge", "nightgauge", nil, true)
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
