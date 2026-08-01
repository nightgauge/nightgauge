package orchestrator

import "testing"

func TestIsTrustedAuthor_DefaultSet(t *testing.T) {
	cases := []struct {
		assoc string
		want  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"FIRST_TIMER", false},
		{"NONE", false},
		{"MANNEQUIN", false},
		{"", false},
		{"SOME_FUTURE_VALUE", false},
		// Case-insensitivity and whitespace tolerance.
		{"owner", true},
		{" Member ", true},
		{"collaborator", true},
	}
	for _, c := range cases {
		if got := isTrustedAuthor(c.assoc, nil); got != c.want {
			t.Errorf("isTrustedAuthor(%q, nil) = %v, want %v", c.assoc, got, c.want)
		}
	}
}

func TestIsTrustedAuthor_ConfiguredOverride(t *testing.T) {
	configured := []string{"CONTRIBUTOR", "MEMBER"}

	// The configured list fully overrides the default set — OWNER is no
	// longer trusted unless explicitly listed.
	if isTrustedAuthor("OWNER", configured) {
		t.Error("OWNER should not be trusted when configured list omits it")
	}
	if !isTrustedAuthor("CONTRIBUTOR", configured) {
		t.Error("CONTRIBUTOR should be trusted per configured override")
	}
	if !isTrustedAuthor("MEMBER", configured) {
		t.Error("MEMBER should be trusted per configured override")
	}
	if isTrustedAuthor("COLLABORATOR", configured) {
		t.Error("COLLABORATOR should not be trusted — not in configured override")
	}
	// Case-insensitive match against configured entries too.
	if !isTrustedAuthor("contributor", configured) {
		t.Error("configured override match should be case-insensitive")
	}
}

func TestIsTrustedAuthor_EmptyConfiguredFallsBackToDefault(t *testing.T) {
	if !isTrustedAuthor("OWNER", []string{}) {
		t.Error("empty configured slice should fall back to the default trusted set")
	}
	if isTrustedAuthor("NONE", []string{}) {
		t.Error("empty configured slice should still fail closed on untrusted values")
	}
}

func TestIsTrustedAuthor_FailsClosedOnWhitespaceOnly(t *testing.T) {
	if isTrustedAuthor("   ", nil) {
		t.Error("whitespace-only author_association must be treated as untrusted")
	}
}
