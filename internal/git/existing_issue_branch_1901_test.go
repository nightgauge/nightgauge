package git

import (
	"strings"
	"testing"
)

// #1901: one issue, one branch. The fixture holds feat/123-stale (local and
// origin), feat/999-live, feat/777-other and wip/999-operator.
func TestExistingIssueBranch(t *testing.T) {
	r := setupLiveRunRepo(t)
	svc := r.service(t, r.primary)

	cases := []struct{ composed, want string }{
		// An earlier name for the same issue wins over a freshly composed one.
		{"feat/123-stale-but-retitled", "feat/123-stale"},
		{"fix/123-other-prefix-too", "feat/123-stale"},
		// The composed name itself is kept.
		{"feat/123-stale", "feat/123-stale"},
		// No branch for the issue: the composed name. wip/ is not an issue branch.
		{"feat/555-new", "feat/555-new"},
		{"feat/9999-not-999", "feat/9999-not-999"},
	}
	for _, tc := range cases {
		got, err := svc.ExistingIssueBranch(tc.composed)
		if err != nil {
			t.Fatalf("ExistingIssueBranch(%q): %v", tc.composed, err)
		}
		if got != tc.want {
			t.Errorf("ExistingIssueBranch(%q) = %q, want %q", tc.composed, got, tc.want)
		}
	}

	// A second, origin-only name for #123 makes the issue ambiguous.
	gitExecTest(t, r.primary, "push", "-q", "origin", "main:refs/heads/fix/123-stale-truncated")
	if got, err := svc.ExistingIssueBranch("feat/123-third"); err == nil ||
		!strings.Contains(err.Error(), "fix/123-stale-truncated") {
		t.Errorf("ExistingIssueBranch with two branches for #123 = %q, %v; want an error naming both", got, err)
	}
}
