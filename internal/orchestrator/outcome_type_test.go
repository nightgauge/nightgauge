package orchestrator

import "testing"

func TestOutcomeTypeForTerminalFailure(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"empty → no outcome type", "", ""},
		{
			"required-check config mismatch → blocked",
			`pr-merge BLOCKED by repository configuration (non-retryable): required-check-config-mismatch:Sentry Smoke (integration). state: OPEN`,
			OutcomeTypeBlocked,
		},
		{
			"graphql required-check rejection → blocked",
			`Required status check "Sentry Smoke (integration)" is expected.`,
			OutcomeTypeBlocked,
		},
		{"merge conflict → no outcome type (not a repo-config block)", `merge conflict in lib/foo.dart`, ""},
		{"generic crash → no outcome type", `exit 1: subagent crashed`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OutcomeTypeForTerminalFailure(tc.text); got != tc.want {
				t.Errorf("OutcomeTypeForTerminalFailure(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}
