package orchestrator

import "testing"

// TestEscalationBlockedByCategory_FilesystemEACCESNotBlocked is the red test
// for #1447: a plain filesystem EACCES line (Go's standard os.OpenFile error
// text, with no git/ssh/forge context at all) must NOT be treated as the
// forge-auth permission denial #878's gate exists to catch. A filesystem
// permission fault is capability-fixable (the agent tried to write somewhere
// it should not have); blocking escalation on it defeats the whole point of
// escalating to a stronger model.
func TestEscalationBlockedByCategory_FilesystemEACCESNotBlocked(t *testing.T) {
	line := "open /var/lib/nightgauge/scratch/abc123.txt: permission denied"
	blocked, reason := EscalationBlockedByCategory(line)
	if blocked {
		t.Fatalf("EscalationBlockedByCategory(%q) = blocked (reason %q); want NOT blocked — this is a filesystem EACCES, not a forge/git-auth denial", line, reason)
	}
}

// TestEscalationBlockedByCategory_ForgeAuthDenialStillBlocked preserves the
// original #878 behavior: a genuine forge/git-transport permission denial
// (the canonical single-method OpenSSH form) must still block escalation.
func TestEscalationBlockedByCategory_ForgeAuthDenialStillBlocked(t *testing.T) {
	text := "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."
	blocked, reason := EscalationBlockedByCategory(text)
	if !blocked {
		t.Fatalf("EscalationBlockedByCategory(%q) = not blocked; want blocked (forge/git-transport auth denial, #878)", text)
	}
	if reason == "" {
		t.Fatalf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
	}
}

// TestEscalationBlockedByCategory_MultiMethodSSHDenialsStillBlocked covers the
// method-list shapes OpenSSH actually emits when a server advertises more
// than one auth method — the normal case for self-hosted GitLab, Gerrit, and
// generic SSH remotes (docs/SELF_HOSTED_GITLAB_SETUP.md,
// docs/FORGE_ABSTRACTION.md). A prior version of this fix narrowed the needle
// to "permission denied (publickey)" with the closing paren immediately
// after "publickey", which fails every one of these; the bare "permission
// denied" phrase plus the filesystem negative-guard must still catch them
// all.
func TestEscalationBlockedByCategory_MultiMethodSSHDenialsStillBlocked(t *testing.T) {
	cases := []string{
		"git@gitlab.example.com: Permission denied (publickey,gssapi-keyex,gssapi-with-mic).\nfatal: Could not read from remote repository.",
		"Permission denied (publickey,password).\nfatal: Could not read from remote repository.",
		"Permission denied (publickey,keyboard-interactive).\nfatal: Could not read from remote repository.",
		"Permission denied, please try again.",
	}
	for _, text := range cases {
		blocked, reason := EscalationBlockedByCategory(text)
		if !blocked {
			t.Errorf("EscalationBlockedByCategory(%q) = not blocked; want blocked (forge/git-transport auth denial)", text)
			continue
		}
		if reason == "" {
			t.Errorf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
		}
	}
}

// TestEscalationBlockedByCategory_ForgeSentinelStillBlocked covers this
// repo's own forge permission sentinel and gh's denial spellings
// (internal/forge/errors.go's ErrPermissionDenied, and the GraphQL/REST
// shapes fixtured in internal/github/issues_test.go and prs_test.go) — a
// board/issue mutation refused for lack of scope must still block
// escalation, same as a git-transport denial.
func TestEscalationBlockedByCategory_ForgeSentinelStillBlocked(t *testing.T) {
	cases := []string{
		"read alerts: forge: permission denied",
		"gh: Permission denied",
		"GraphQL: Permission denied (addProjectV2ItemById)",
	}
	for _, text := range cases {
		blocked, reason := EscalationBlockedByCategory(text)
		if !blocked {
			t.Errorf("EscalationBlockedByCategory(%q) = not blocked; want blocked (forge permission denial)", text)
			continue
		}
		if reason == "" {
			t.Errorf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
		}
	}
}

// TestEscalationBlockedByCategory_ModelProviderDenialsStillBlocked covers the
// model-provider auth denials documented as real symptoms in
// docs/TROUBLESHOOTING.md — escalating to a stronger model the project has no
// IAM grant for re-dispatches the whole prompt to fail identically, exactly
// #878's observed waste.
func TestEscalationBlockedByCategory_ModelProviderDenialsStillBlocked(t *testing.T) {
	cases := []string{
		"Permission denied to access model",
		"google: Permission denied on resource project foo.",
	}
	for _, text := range cases {
		blocked, reason := EscalationBlockedByCategory(text)
		if !blocked {
			t.Errorf("EscalationBlockedByCategory(%q) = not blocked; want blocked (provider permission denial)", text)
			continue
		}
		if reason == "" {
			t.Errorf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
		}
	}
}

// TestFirstCauseFromOutputTail_FilesystemEACCESNotPickedAsCause covers AC3:
// firstCauseFromOutputTail must not pick a plain filesystem EACCES line as a
// run's recorded cause.
func TestFirstCauseFromOutputTail_FilesystemEACCESNotPickedAsCause(t *testing.T) {
	tail := "writing output\nopen /var/lib/nightgauge/scratch/abc123.txt: permission denied\nstage exited 1"
	if got := firstCauseFromOutputTail(tail); got != "" {
		t.Fatalf("firstCauseFromOutputTail(%q) = %q; want \"\" — a filesystem EACCES is not a forge-auth cause", tail, got)
	}
}

// TestFirstCauseFromOutputTail_ForgeAuthDenialStillPickedAsCause preserves the
// original #878 behavior for the cause-naming helper.
func TestFirstCauseFromOutputTail_ForgeAuthDenialStillPickedAsCause(t *testing.T) {
	tail := "attempting push\ngit@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."
	want := "git@github.com: Permission denied (publickey)."
	if got := firstCauseFromOutputTail(tail); got != want {
		t.Fatalf("firstCauseFromOutputTail(%q) = %q; want %q", tail, got, want)
	}
}
