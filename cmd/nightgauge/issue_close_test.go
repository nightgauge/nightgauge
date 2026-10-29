package main

import "testing"

// TestIssueCloseSkipReason covers the --pr decision of `issue close` (#2674):
// only a read body without a closing keyword for the issue skips the close.
func TestIssueCloseSkipReason(t *testing.T) {
	cases := []struct {
		name string
		body string
		skip bool
	}{
		{"refs", "Implements #600\n\nRefs #600\n", true},
		{"other issue closed", "Closes #601\nRefs #600\n", true},
		{"closes", "Closes #600\n", false},
		{"fixes with colon", "Fixes: #600\n", false},
		{"resolved", "resolved #600", false},
		{"repo qualified", "Closes nightgauge/nightgauge#600", false},
		{"empty body", "", false},
		{"blank body", "  \n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := issueCloseSkipReason(tc.body, 600)
			if skip != tc.skip {
				t.Fatalf("skip = %v, want %v", skip, tc.skip)
			}
			if skip && reason != "partial_delivery" {
				t.Errorf("reason = %q, want partial_delivery", reason)
			}
			if !skip && reason != "" {
				t.Errorf("reason = %q, want empty", reason)
			}
		})
	}
}
