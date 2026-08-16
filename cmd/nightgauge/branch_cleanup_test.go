package main

import (
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

func TestGitBranchCleanupCmd_HelpListsDocsAndChore(t *testing.T) {
	cmd := gitBranchCleanupCmd()
	long := cmd.Long
	for _, prefix := range []string{"docs/", "chore/", "feat/", "epic/"} {
		if !strings.Contains(long, prefix) {
			t.Errorf("branch-cleanup help must mention %s:\n%s", prefix, long)
		}
	}
}

func TestGitBranchCleanup_PrefixAllowlist(t *testing.T) {
	// CLI contract: GIT_WORKFLOW prefixes + epic/; never operator wip/.
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "docs/583-example", want: true},
		{branch: "chore/583-example", want: true},
		{branch: "feat/583-thing", want: true},
		{branch: "wip/583-example", want: false},
		{branch: "feat/no-number", want: false},
	}
	for _, tt := range tests {
		if got := gitpkg.IsCleanupCandidate(tt.branch); got != tt.want {
			t.Errorf("IsCleanupCandidate(%q) = %v, want %v", tt.branch, got, tt.want)
		}
	}
}
