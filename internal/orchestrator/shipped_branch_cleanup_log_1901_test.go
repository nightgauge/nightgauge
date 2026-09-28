package orchestrator

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
)

// #1901: the post-cleanup line reports what happened. A branch that was
// deleted, or was already gone, is never reported as "NOT removed locally" or
// as "the only copy".
func TestDescribeShippedBranchCleanup(t *testing.T) {
	const b = "feat/1-x"
	cases := []struct {
		name    string
		res     execution.ShippedBranchCleanup
		want    string
		mustNot []string
	}{
		{"already gone", execution.ShippedBranchCleanup{}, "already gone", []string{"NOT removed", "only copy"}},
		{"deleted both", execution.ShippedBranchCleanup{LocalBefore: true, RemoteBefore: true, LocalDeleted: true, RemoteDeleted: true},
			"cleaned up feature branch", []string{"NOT removed", "only copy"}},
		{"deleted origin-only", execution.ShippedBranchCleanup{RemoteBefore: true, RemoteDeleted: true},
			"cleaned up feature branch", []string{"NOT removed", "only copy"}},
		{"declined", execution.ShippedBranchCleanup{LocalBefore: true, Declined: true},
			"kept (local)", []string{"origin"}},
		{"local delete refused", execution.ShippedBranchCleanup{LocalBefore: true, RemoteBefore: true, RemoteDeleted: true},
			"still present: local", []string{"origin"}},
	}
	for _, tc := range cases {
		got := describeShippedBranchCleanup(b, tc.res)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, got, tc.want)
		}
		for _, bad := range tc.mustNot {
			if strings.Contains(got, bad) {
				t.Errorf("%s: %q must not contain %q", tc.name, got, bad)
			}
		}
	}
}
