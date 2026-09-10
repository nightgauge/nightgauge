package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/cmd/backlogpreflight"
)

func TestRenderPreflightHumanIncludesRemediation(t *testing.T) {
	report := backlogpreflight.BacklogPreflightReport{
		Status: "Ready",
		Findings: []backlogpreflight.BacklogFinding{
			{
				FindingType: backlogpreflight.FindingTypeGreenfieldWarning,
				Detail:      "Missing .nightgauge/complexity-model.yaml",
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
