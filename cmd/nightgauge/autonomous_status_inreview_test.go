package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// An "In review" item with no open PR is rejected on every scan and recorded
// nowhere else, so `autonomous status` must list it (#1496).
func TestAutonomousStatusListsInReviewWithoutPR(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status: "running",
		InReviewWithoutPR: []orchestrator.InReviewWithoutPRItem{
			{Repo: "octocat/acme-app", Number: 312, Title: "stranded after a failed run"},
		},
	})

	out := runAutonomousStatus(t, root)
	if !strings.Contains(out, "In review with no open PR (1)") ||
		!strings.Contains(out, "octocat/acme-app#312") ||
		!strings.Contains(out, "stranded after a failed run") {
		t.Errorf("status does not list the In review item with no open PR:\n%s", out)
	}
}

func TestAutonomousStatusOmitsInReviewWithoutPRWhenEmpty(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{Status: "running"})

	if out := runAutonomousStatus(t, root); strings.Contains(out, "no open PR") {
		t.Errorf("status prints an empty no-open-PR section:\n%s", out)
	}
}

func TestAutonomousStatusJSONCarriesInReviewWithoutPR(t *testing.T) {
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, orchestrator.AutonomousState{
		Status: "running",
		InReviewWithoutPR: []orchestrator.InReviewWithoutPRItem{
			{Repo: "octocat/acme-app", Number: 312},
		},
	})
	t.Chdir(root)
	cmd := autonomousStatusCmd()
	cmd.SetArgs([]string{"--json"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("autonomous status --json: %v", err)
		}
	})
	var got struct {
		InReviewWithoutPR []orchestrator.InReviewWithoutPRItem `json:"inReviewWithoutPR"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse --json output: %v\n%s", err, out)
	}
	if len(got.InReviewWithoutPR) != 1 || got.InReviewWithoutPR[0].Number != 312 {
		t.Errorf("--json inReviewWithoutPR = %+v, want #312", got.InReviewWithoutPR)
	}
}
