package main

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/ipc"
)

// The reload-safety sentence (#1511).
//
// `autonomous stop` lets in-flight slots finish; a VS Code window reload
// aborts them. So `Autonomous Mode: Stopped` on its own reads as "safe to
// reload" at exactly the moment it is not, and an operator acting on that word
// loses every running pipeline. These pin the two halves of the answer.

func TestReloadSafetyLineWhenNothingIsRunning(t *testing.T) {
	got := reloadSafetyLine(nil)
	if got != "0 running; safe to reload" {
		t.Errorf("reloadSafetyLine(nil) = %q", got)
	}
}

func TestReloadSafetyLineNamesEveryRunningIssue(t *testing.T) {
	got := reloadSafetyLine([]ipc.RunningPipelineInfo{
		{Repo: "acme/acme-flutter", IssueNumber: 313, Source: "autonomous"},
		{Repo: "acme/acme-platform", IssueNumber: 1429, Source: "manual"},
	})

	// The count and the identities both matter: "2 running" alone does not
	// tell an operator whether the work is theirs to wait for.
	if !strings.Contains(got, "2 pipeline(s) still running") {
		t.Errorf("missing the count: %q", got)
	}
	if !strings.Contains(got, "#313 acme-flutter") || !strings.Contains(got, "#1429 acme-platform") {
		t.Errorf("missing an issue identity: %q", got)
	}
	if !strings.Contains(got, "reload is not safe yet") {
		t.Errorf("missing the verdict: %q", got)
	}
}

func TestReloadSafetyLineFlagsAStaleRunRatherThanHidingIt(t *testing.T) {
	got := reloadSafetyLine([]ipc.RunningPipelineInfo{
		{Repo: "acme/web", IssueNumber: 7, Stale: true},
	})

	// Dropping a stale row would manufacture "safe to reload" over work that
	// may still be running. It is annotated instead.
	if !strings.Contains(got, "#7 web") {
		t.Errorf("a stale run must still be listed: %q", got)
	}
	if !strings.Contains(got, "no progress recently") {
		t.Errorf("a stale run must be marked as such: %q", got)
	}
	if !strings.Contains(got, "reload is not safe yet") {
		t.Errorf("a stale run still blocks the safe verdict: %q", got)
	}
}

func TestShortRepoName(t *testing.T) {
	cases := map[string]string{
		"acme/acme-flutter": "acme-flutter",
		"acme-flutter":      "acme-flutter",
		"":                  "",
	}
	for in, want := range cases {
		if got := shortRepoName(in); got != want {
			t.Errorf("shortRepoName(%q) = %q, want %q", in, got, want)
		}
	}
}
