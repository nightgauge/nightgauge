package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/backlogpreflight"
)

// TestBacklogPreflightErrorsExitTwo pins the documented exit contract: 1 is
// "findings found", so an error exits 2. When an error exited 1, a skill
// reading the exit code took a failed board read (a blocker list that could
// not be read whole, #1701) for a findings verdict with no findings in it.
func TestBacklogPreflightErrorsExitTwo(t *testing.T) {
	cmd := backlogPreflightCmd()
	var stderr bytes.Buffer
	err := errors.New("fetch board items: relationship connection truncated")
	if code := exitCodeFor(&stderr, cmd, err); code != exitCouldNotRun {
		t.Fatalf("exit = %d, want %d", code, exitCouldNotRun)
	}
	if !strings.HasPrefix(stderr.String(), "could not run: ") {
		t.Errorf("stderr = %q, want a could-not-run line", stderr.String())
	}

	// cobra's own flag errors, which RunE never sees, exit 2 as well.
	root := rootCmd()
	root.SetArgs([]string{"backlog", "preflight", "--no-such-flag"})
	var cobraOut bytes.Buffer
	root.SetOut(&cobraOut)
	root.SetErr(&cobraOut)
	ran, err := root.ExecuteC()
	if code := exitCodeFor(&bytes.Buffer{}, ran, err); code != exitCouldNotRun {
		t.Fatalf("unknown flag: exit = %d, want %d (err = %v)", code, exitCouldNotRun, err)
	}
}

func TestRenderPreflightHumanIncludesRemediation(t *testing.T) {
	report := backlogpreflight.BacklogPreflightReport{
		Status: "Ready",
		Findings: []backlogpreflight.BacklogFinding{
			{
				FindingType: backlogpreflight.FindingTypeGreenfieldWarning,
				Detail:      "Missing .git/nightgauge-worktree/complexity-model.yaml",
				Suggestion:  "Run: nightgauge outcome init to generate the complexity model",
			},
		},
		Summary: backlogpreflight.Summary{IssuesFlagged: 1},
	}

	output := captureStdout(t, func() { renderPreflightHuman(report) })
	if !strings.Contains(output, "nightgauge outcome init") {
		t.Fatalf("human preflight output hides remediation:\n%s", output)
	}
}
