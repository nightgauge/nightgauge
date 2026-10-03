package git

import "testing"

// TestEpicBranchParent pins which parent an issue branch is based on (#2377).
// epic/<N>-* names #N of the repository it is pushed to, so only a parent in
// the sub-issue's own repository has an epic branch there.
func TestEpicBranchParent(t *testing.T) {
	cases := []struct {
		name       string
		subRepo    string
		parent     int
		parentRepo string
		want       int
	}{
		{"no parent", "example-org/app", 0, "", 0},
		{"parent in the sub-issue's repository", "example-org/app", 20, "example-org/app", 20},
		{"repositories compare case-insensitively", "Example-Org/App", 20, "example-org/app", 20},
		{"an unrecorded parent repository is the sub-issue's own", "example-org/app", 20, "", 20},
		{"a parent in another repository has no epic branch here", "example-org/app", 20, "example-org/platform", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EpicBranchParent(c.subRepo, c.parent, c.parentRepo); got != c.want {
				t.Errorf("EpicBranchParent(%q, %d, %q) = %d, want %d",
					c.subRepo, c.parent, c.parentRepo, got, c.want)
			}
		})
	}
}
