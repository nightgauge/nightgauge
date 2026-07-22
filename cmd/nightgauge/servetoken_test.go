package main

import (
	"os"
	"testing"
)

// fakeTokenResolver returns a fixed token, short-circuiting ResolveTokenChain
// at the config step so the test never shells out to `gh` (deterministic).
type fakeTokenResolver struct{ tok string }

func (f fakeTokenResolver) ResolveToken(_ string) (string, error) { return f.tok, nil }
func (f fakeTokenResolver) SuppressGHWarning() bool               { return true }

// TestExportConfiguredGitHubToken verifies the resolved config token is
// exported to both GH_TOKEN and GITHUB_TOKEN so deterministic `gh` subprocesses
// (gates, board-status) authenticate as the pipeline identity rather than the
// machine's ambient gh account (#3887).
func TestExportConfiguredGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	got := exportConfiguredGitHubToken(fakeTokenResolver{tok: "ghp_pipeline_identity"}, "nightgauge")

	if got != "ghp_pipeline_identity" {
		t.Fatalf("returned token = %q, want ghp_pipeline_identity", got)
	}
	if v := os.Getenv("GH_TOKEN"); v != "ghp_pipeline_identity" {
		t.Errorf("GH_TOKEN = %q, want ghp_pipeline_identity", v)
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "ghp_pipeline_identity" {
		t.Errorf("GITHUB_TOKEN = %q, want ghp_pipeline_identity", v)
	}
}

// TestMaybeExportGitHubToken_ExportsWhenUnset verifies the PersistentPreRunE
// helper resolves and exports the per-repo token when GH_TOKEN is not already
// set — making every subcommand self-sufficient for the repo's gh identity.
func TestMaybeExportGitHubToken_ExportsWhenUnset(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	got := maybeExportGitHubToken(fakeTokenResolver{tok: "ghp_repo_identity"}, "nightgauge")

	if got != "ghp_repo_identity" {
		t.Fatalf("returned token = %q, want ghp_repo_identity", got)
	}
	if v := os.Getenv("GH_TOKEN"); v != "ghp_repo_identity" {
		t.Errorf("GH_TOKEN = %q, want ghp_repo_identity", v)
	}
}

// TestMaybeExportGitHubToken_SkipsWhenAlreadySet verifies the helper does NOT
// override a caller-provided GH_TOKEN (extension/terminal env, hook, skillRunner)
// and skips the resolution on the hot path — for a SINGLE-IDENTITY repo (no
// configured github_user). The fakeTokenResolver does not expose
// ResolveGitHubUserForOwner, so this exercises the "no configured identity"
// branch where the ambient token wins.
func TestMaybeExportGitHubToken_SkipsWhenAlreadySet(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_caller_supplied")
	t.Setenv("GITHUB_TOKEN", "ghp_caller_supplied")

	got := maybeExportGitHubToken(fakeTokenResolver{tok: "ghp_should_be_ignored"}, "nightgauge")

	if got != "" {
		t.Errorf("returned token = %q, want \"\" (skipped — caller value wins)", got)
	}
	if v := os.Getenv("GH_TOKEN"); v != "ghp_caller_supplied" {
		t.Errorf("GH_TOKEN = %q, want ghp_caller_supplied (unchanged)", v)
	}
}

// identityTokenResolver is a resolver that exposes a configured per-owner
// github_user (#4068), so maybeExportGitHubToken treats the per-repo identity as
// authoritative over the ambient env token.
type identityTokenResolver struct {
	tok          string
	usersByOwner map[string]string
}

func (r identityTokenResolver) ResolveToken(_ string) (string, error) { return r.tok, nil }
func (r identityTokenResolver) SuppressGHWarning() bool               { return true }
func (r identityTokenResolver) ResolveGitHubUserForOwner(owner string) string {
	return r.usersByOwner[owner]
}

// TestMaybeExportGitHubToken_OverridesAmbientWhenIdentityConfigured verifies the
// #4068 authority rule: when the repo configures a specific github_user, an
// ambient (wrong-user) GH_TOKEN is OVERRIDDEN by the resolved per-repo token —
// the configured identity is authoritative, not the ambient env.
func TestMaybeExportGitHubToken_OverridesAmbientWhenIdentityConfigured(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_ambient_wrong_user")
	t.Setenv("GITHUB_TOKEN", "ghp_ambient_wrong_user")

	resolver := identityTokenResolver{
		tok:          "ghp_acmebot_identity",
		usersByOwner: map[string]string{"Acme-Community": "acmebot"},
	}
	got := maybeExportGitHubToken(resolver, "Acme-Community")

	if got != "ghp_acmebot_identity" {
		t.Fatalf("returned token = %q, want ghp_acmebot_identity (override ambient)", got)
	}
	if v := os.Getenv("GH_TOKEN"); v != "ghp_acmebot_identity" {
		t.Errorf("GH_TOKEN = %q, want ghp_acmebot_identity (ambient overridden)", v)
	}
}

// TestMaybeExportGitHubToken_NoIdentityKeepsAmbient verifies that for an owner
// with no configured github_user, the ambient token is preserved even though the
// resolver exposes ResolveGitHubUserForOwner (the map simply has no entry).
func TestMaybeExportGitHubToken_NoIdentityKeepsAmbient(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_caller_supplied")
	t.Setenv("GITHUB_TOKEN", "ghp_caller_supplied")

	resolver := identityTokenResolver{
		tok:          "ghp_should_be_ignored",
		usersByOwner: map[string]string{"Acme-Community": "acmebot"},
	}
	got := maybeExportGitHubToken(resolver, "nightgauge") // owner not in the map

	if got != "" {
		t.Errorf("returned token = %q, want \"\" (no configured identity → ambient wins)", got)
	}
	if v := os.Getenv("GH_TOKEN"); v != "ghp_caller_supplied" {
		t.Errorf("GH_TOKEN = %q, want ghp_caller_supplied (unchanged)", v)
	}
}
