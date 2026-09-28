package issueslug

import "testing"

func TestOf(t *testing.T) {
	for title, want := range map[string]string{
		"security: verify zero non-loopback egress for OpenCode + local provider": "security-verify-zero-non-loopback-egress-for-openc",
		"Don't_break   it!": "don-t-break-it",
		"!!!":               "",
		"0123456789012345678901234567890123456789012345678-x": "0123456789012345678901234567890123456789012345678",
	} {
		if got := Of(title); got != want {
			t.Errorf("Of(%q) = %q, want %q", title, got, want)
		}
		if len(Of(title)) > MaxLen {
			t.Errorf("Of(%q) exceeds MaxLen", title)
		}
	}
}

func TestForIssue(t *testing.T) {
	cases := []struct {
		n           int
		title, want string
	}{
		{227, "#227 Fix the thing", "fix-the-thing"},
		{12, "404 page returns 500", "404-page-returns-500"},
		{22, "2200 requests", "2200-requests"},
		{42, "42", ""},
		{1644, "security: verify zero non-loopback egress for OpenCode + local provider",
			"security-verify-zero-non-loopback-egress-for-openc"},
	}
	for _, tc := range cases {
		if got := ForIssue(tc.n, tc.title); got != tc.want {
			t.Errorf("ForIssue(%d, %q) = %q, want %q", tc.n, tc.title, got, tc.want)
		}
	}
	// Branch slug and knowledge slug agree for a title not opening with its number.
	title := "security: verify zero non-loopback egress for OpenCode + local provider"
	if ForIssue(1644, title) != Of(title) {
		t.Error("branch and knowledge slugs diverge for #1644's title")
	}
}
