package github

import "testing"

// A stage forces CI=true and carries the operator's own value in
// NIGHTGAUGE_HOST_CI, which wins (#2514); without it CI decides.
func TestCIHostPrefersHostValue(t *testing.T) {
	cases := []struct {
		ci, host string
		want     bool
	}{
		{"", "", false},
		{"true", "", true},
		{"1", "", true},
		{"true", "false", false},
		{"true", "true", true},
	}
	for _, tc := range cases {
		t.Setenv("CI", tc.ci)
		t.Setenv("NIGHTGAUGE_HOST_CI", tc.host)
		if got := ciHost(); got != tc.want {
			t.Errorf("ciHost() with CI=%q NIGHTGAUGE_HOST_CI=%q = %v, want %v", tc.ci, tc.host, got, tc.want)
		}
	}
}
