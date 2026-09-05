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
// must still block escalation.
func TestEscalationBlockedByCategory_ForgeAuthDenialStillBlocked(t *testing.T) {
	text := "remote: Permission denied (publickey).\nfatal: Could not read from remote repository."
	blocked, reason := EscalationBlockedByCategory(text)
	if !blocked {
		t.Fatalf("EscalationBlockedByCategory(%q) = not blocked; want blocked (forge/git-transport auth denial, #878)", text)
	}
	if reason == "" {
		t.Fatalf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
	}
}

// TestEscalationBlockedByCategory_ForgeAuthPasswordDenialStillBlocked covers
// the password-auth (non-publickey) shape of a git-transport denial. This
// line matches ONLY the "remote: permission denied" entry — no other entry
// in permissionPhrases fires for it — so this test exists specifically to
// keep that entry from being silently removable.
func TestEscalationBlockedByCategory_ForgeAuthPasswordDenialStillBlocked(t *testing.T) {
	text := "remote: Permission denied, please try again."
	blocked, reason := EscalationBlockedByCategory(text)
	if !blocked {
		t.Fatalf("EscalationBlockedByCategory(%q) = not blocked; want blocked (forge/git-transport password auth denial)", text)
	}
	if reason == "" {
		t.Fatalf("EscalationBlockedByCategory(%q) returned blocked=true but an empty reason", text)
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
	tail := "attempting push\nremote: Permission denied (publickey).\nfatal: Could not read from remote repository."
	want := "remote: Permission denied (publickey)."
	if got := firstCauseFromOutputTail(tail); got != want {
		t.Fatalf("firstCauseFromOutputTail(%q) = %q; want %q", tail, got, want)
	}
}
