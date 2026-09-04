package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Who a CLI action is attributed to (#1418).
//
// The chain used to be --actor -> $USER -> "cli", with a doc comment claiming
// $USER kept a local action "attributable". That is the wrong way round: a
// macOS account name is an identity on exactly one machine and resolves to
// nobody anywhere else, including on the platform, which mirrors this field.
// It produced two spellings of one human in the audit trail on this machine —
// 23 rows under the GitHub login, 4 under the OS name — and only one of them
// names an account that exists.

func writeConfigWithUser(t *testing.T, root, user string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "owner: testorg\n"
	if user != "" {
		body += "github_user: " + user + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestAttentionActor_PrefersTheExplicitFlag(t *testing.T) {
	root := t.TempDir()
	writeConfigWithUser(t, root, "config-login")

	if got := attentionActor("octocat", root); got != "octocat" {
		t.Errorf("attentionActor = %q, want the explicit --actor", got)
	}
}

func TestAttentionActor_FallsBackToTheConfiguredGitHubLogin(t *testing.T) {
	root := t.TempDir()
	writeConfigWithUser(t, root, "markamccorkle")

	// $USER is deliberately set to the OTHER spelling. The whole point is that
	// it must be ignored, not merely ranked below.
	t.Setenv("USER", "markmccorkle")

	got := attentionActor("", root)
	if got != "markamccorkle" {
		t.Errorf("attentionActor = %q, want the configured GitHub login", got)
	}
	if got == "markmccorkle" {
		t.Error("the OS username reached the audit trail — it resolves to no account off this machine")
	}
}

func TestAttentionActor_IgnoresTheOSUsernameEntirely(t *testing.T) {
	// No config at all: the OS name must STILL not be used. Demoting $USER
	// rather than dropping it would recreate the ambiguity on any machine where
	// the OS name and the login differ, which is every machine but one.
	root := t.TempDir()
	t.Setenv("USER", "some-local-account")

	if got := attentionActor("", root); got != "cli" {
		t.Errorf("attentionActor = %q, want \"cli\" — an OS username is not attribution", got)
	}
}

// TestAttentionActor_IsNeverEmpty is the interaction with #1405.
//
// That change refuses an empty actor at the store boundary, which makes this
// chain load-bearing: a rung that can yield "" fails the resolve outright
// instead of recording a poor name. The terminal rung must be non-empty BY
// CONSTRUCTION, not by luck.
func TestAttentionActor_IsNeverEmpty(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"no config, no USER": func(t *testing.T) string {
			t.Setenv("USER", "")
			return t.TempDir()
		},
		"config present but github_user empty": func(t *testing.T) string {
			root := t.TempDir()
			writeConfigWithUser(t, root, "")
			return root
		},
		"unreadable root": func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "does-not-exist")
		},
		"whitespace actor": func(t *testing.T) string { return t.TempDir() },
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := setup(t)
			actor := ""
			if name == "whitespace actor" {
				actor = "   "
			}
			if got := attentionActor(actor, root); got == "" {
				t.Error("attentionActor returned empty — #1405 refuses that at the store " +
					"boundary, so the resolve fails outright instead of recording who acted")
			}
		})
	}
}
