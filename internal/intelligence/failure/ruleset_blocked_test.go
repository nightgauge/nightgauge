package failure

import "testing"

func TestIsRulesetBlocked(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{
			"config mismatch blocker (pr ruleset-precheck)",
			`required-check-config-mismatch:Sentry Smoke (integration)`,
			true,
		},
		{
			"config mismatch inside a longer terminal message",
			`pr-merge BLOCKED by repository configuration (non-retryable): required-check-config-mismatch:Sentry Smoke (integration). state: OPEN`,
			true,
		},
		{
			"graphql required-check rejection",
			`Required status check "Sentry Smoke (integration)" is expected.`,
			true,
		},
		{
			"required checks failing",
			`the following required status checks are failing: build`,
			true,
		},
		{
			"probe output 'no required status checks' must NOT classify",
			`No required status checks found for this branch`,
			false,
		},
		{"unrelated merge conflict", `merge conflict in lib/foo.dart`, false},
		{"generic subagent crash", `exit 1: subagent crashed`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRulesetBlocked(tc.text); got != tc.want {
				t.Errorf("IsRulesetBlocked(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
