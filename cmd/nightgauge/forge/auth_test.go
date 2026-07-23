package forgecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// TestAuthTokenCmd_PrintsConfigToken verifies `forge auth token` prints the
// config-resolved token (the config-token path short-circuits the chain, so no
// `gh` subprocess runs — deterministic). This is the token skills and the
// VSCode extension host export to authenticate their own gh subprocesses
// (#3892).
func TestAuthTokenCmd_PrintsConfigToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgYAML := "owner: TestOrg\ngithub_auth:\n  token: direct-test-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(dir)

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "token"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "direct-test-token" {
		t.Errorf("token output = %q, want direct-test-token", got)
	}
}

// TestAuthTokenCmd_IdentityOnly verifies the #4068 guard.sh override gate: with
// --identity-only, a token is emitted ONLY when the repo configures a per-repo
// github_user; a repo with no configured identity prints nothing so guard.sh
// leaves the ambient GH_TOKEN untouched (never clobbering it with the default
// gh account).
func TestAuthTokenCmd_IdentityOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from the machine ~/.nightgauge github_user

	run := func(t *testing.T, cfgYAML string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".nightgauge"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".nightgauge", "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Chdir(dir)
		root := Cmd()
		stdout := &bytes.Buffer{}
		root.SetOut(stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"auth", "token", "--identity-only"})
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return strings.TrimSpace(stdout.String())
	}

	t.Run("no github_user prints nothing", func(t *testing.T) {
		// A config token is present but NO github_user → no per-repo identity →
		// --identity-only emits nothing (guard.sh keeps the ambient token).
		if got := run(t, "owner: TestOrg\ngithub_auth:\n  token: direct-test-token\n"); got != "" {
			t.Errorf("identity-only with no github_user = %q, want empty", got)
		}
	})

	t.Run("configured github_user prints the resolved token", func(t *testing.T) {
		// github_user IS configured → emit the resolved token (tier-2 config token
		// here, so no gh subprocess is needed).
		if got := run(t, "owner: TestOrg\ngithub_user: someuser\ngithub_auth:\n  token: identity-tok\n"); got != "identity-tok" {
			t.Errorf("identity-only with github_user = %q, want identity-tok", got)
		}
	})
}

func TestMaskToken(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "****"},
		{"short", "****"},
		{"abcdefghij", "abcd****ghij"},
		{"ghp_xxxxyyyyzzzz", "ghp_****zzzz"},
	}
	for _, tt := range tests {
		got := MaskToken(tt.in)
		if got != tt.want {
			t.Errorf("MaskToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAuthStatus_NeverLeaksToken(t *testing.T) {
	rawToken := "ghp_super_secret_value_xyz"
	withFakeForge(t, &fakeForge{
		auth: &fakeAuthService{
			resp: &forgetypes.TokenScopeInfo{
				Login:      "alice",
				Scopes:     []string{"repo", "project"},
				Resolution: "config",
				Valid:      true,
			},
		},
	})
	// Override the source helper to simulate a real token in env.
	origSource := authSourceAndMaskedToken
	authSourceAndMaskedToken = func() (string, string) { return "env", MaskToken(rawToken) }
	defer func() { authSourceAndMaskedToken = origSource }()

	root := Cmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs([]string{"auth", "status", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, rawToken) {
		t.Errorf("raw token leaked into output: %q", combined)
	}
	var got AuthStatusJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Login != "alice" {
		t.Errorf("login = %q", got.Login)
	}
	if !got.Valid {
		t.Errorf("valid should be true")
	}
}

func TestAuthLogin_UsesKeyring(t *testing.T) {
	calls := []string{}
	origStore, origScrub := storeTokenInKeyring, scrubPlaintextCredentials
	storeTokenInKeyring = func(token string) error {
		calls = append(calls, "keyring:"+MaskToken(token))
		return nil
	}
	scrubPlaintextCredentials = func() ([]string, error) { return nil, nil }
	defer func() { storeTokenInKeyring, scrubPlaintextCredentials = origStore, origScrub }()

	withFakeForge(t, &fakeForge{auth: &fakeAuthService{}})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "login", "--token", "ghp_xxxxxxxxyyyyyyyy", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("write calls = %v", calls)
	}
	if strings.Contains(stdout.String(), "ghp_xxxxxxxxyyyyyyyy") {
		t.Errorf("token leaked: %s", stdout.String())
	}
}

func TestAuthLogout_Idempotent(t *testing.T) {
	origRemove, origScrub := removeTokenFromKeyring, scrubPlaintextCredentials
	removeTokenFromKeyring = func() error { return nil }
	scrubPlaintextCredentials = func() ([]string, error) { return nil, nil }
	defer func() { removeTokenFromKeyring, scrubPlaintextCredentials = origRemove, origScrub }()

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "logout", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["loggedOut"] != true {
		t.Errorf("loggedOut should be true: %+v", got)
	}
}

func TestScrubCredentialFileRemovesLiteralsAndPreservesEnvReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "owner: TestOrg\ngithub_auth:\n  token: literal-one\n  tokens:\n    TestOrg: literal-two\n    SafeOrg: env:SAFE_TOKEN\nplatform:\n  license_key: literal-three\n  api_key: literal-four\n  api_url: https://example.invalid\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	changed, err := scrubCredentialFile(path)
	if err != nil || !changed {
		t.Fatalf("scrubCredentialFile changed=%v err=%v", changed, err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scrubbed config: %v", err)
	}
	text := string(out)
	for _, forbidden := range []string{"literal-one", "literal-two"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("plaintext credential remained after scrub")
		}
	}
	if !strings.Contains(text, "env:SAFE_TOKEN") || !strings.Contains(text, "api_url:") {
		t.Fatal("scrub removed safe configuration")
	}
	// Forge auth owns GitHub credentials only. Platform credentials must not be
	// deleted until the VS Code migration has safely copied them to SecretStorage.
	if !strings.Contains(text, "literal-three") || !strings.Contains(text, "literal-four") {
		t.Fatal("forge auth scrubbed credentials it cannot migrate")
	}
}

func TestAuthWhoami_PrintsLogin(t *testing.T) {
	withFakeForge(t, &fakeForge{
		auth: &fakeAuthService{
			resp: &forgetypes.TokenScopeInfo{Login: "alice"},
		},
	})
	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "whoami", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got WhoamiJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if got.Login != "alice" {
		t.Errorf("login = %q, want alice", got.Login)
	}
	if got.V != 1 {
		t.Errorf("v = %d", got.V)
	}
}

// TestAuthAssert covers the deterministic preflight verb (#4068): the verb
// renders the structured result and exits non-zero (with a stderr reason +
// remediation) when the resolved identity is wrong or lacks the required
// access, and exits 0 when it matches. assertIdentity is overridden so the
// cases are deterministic and never touch the network.
func TestAuthAssert(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		res        AuthAssertJSON
		resErr     error
		wantErr    bool
		wantOK     bool
		wantStderr string // substring expected on stderr when !ok
	}{
		{
			name:    "identity matches and has push → exit 0",
			args:    []string{"auth", "assert", "--repo", "Acme-Community/acmesvc-tracker", "--json"},
			res:     AuthAssertJSON{V: 1, OK: true, Repo: "Acme-Community/acmesvc-tracker", ExpectedLogin: "acmebot", ActualLogin: "acmebot", HasPush: true},
			wantErr: false,
			wantOK:  true,
		},
		{
			name:       "wrong resolved identity → non-zero + remediation",
			args:       []string{"auth", "assert", "--repo", "Acme-Community/acmesvc-tracker", "--json"},
			res:        AuthAssertJSON{V: 1, OK: false, Repo: "Acme-Community/acmesvc-tracker", ExpectedLogin: "acmebot", ActualLogin: "octocat", Reason: "resolved identity is \"octocat\" but config expects \"acmebot\"", Remediation: "run: GH_TOKEN=$(env -u GH_TOKEN -u GITHUB_TOKEN gh auth token --user acmebot) gh ..."},
			wantErr:    true,
			wantOK:     false,
			wantStderr: "remediation:",
		},
		{
			name:       "correct identity lacking push → non-zero with blocker",
			args:       []string{"auth", "assert", "--repo", "Acme-Community/acmesvc-tracker", "--json"},
			res:        AuthAssertJSON{V: 1, OK: false, Repo: "Acme-Community/acmesvc-tracker", ExpectedLogin: "acmebot", ActualLogin: "acmebot", HasPush: false, Reason: "identity \"acmebot\" lacks push access on Acme-Community/acmesvc-tracker"},
			wantErr:    true,
			wantOK:     false,
			wantStderr: "lacks push access",
		},
		{
			name:    "admin required and granted → exit 0",
			args:    []string{"auth", "assert", "--repo", "Acme-Community/acmesvc-tracker", "--admin", "--json"},
			res:     AuthAssertJSON{V: 1, OK: true, Repo: "Acme-Community/acmesvc-tracker", ExpectedLogin: "acmebot", ActualLogin: "acmebot", HasPush: true, HasAdmin: true, AdminRequired: true},
			wantErr: false,
			wantOK:  true,
		},
		{
			name:       "admin required but missing → non-zero",
			args:       []string{"auth", "assert", "--repo", "Acme-Community/acmesvc-tracker", "--admin", "--json"},
			res:        AuthAssertJSON{V: 1, OK: false, Repo: "Acme-Community/acmesvc-tracker", ExpectedLogin: "acmebot", ActualLogin: "acmebot", HasPush: true, HasAdmin: false, AdminRequired: true, Reason: "identity \"acmebot\" lacks admin access"},
			wantErr:    true,
			wantOK:     false,
			wantStderr: "lacks admin access",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := assertIdentity
			var gotAdmin bool
			assertIdentity = func(_ context.Context, owner, repo string, requireAdmin bool) (AuthAssertJSON, error) {
				gotAdmin = requireAdmin
				return tt.res, tt.resErr
			}
			defer func() { assertIdentity = orig }()

			root := Cmd()
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			root.SetOut(stdout)
			root.SetErr(stderr)
			root.SetArgs(tt.args)
			err := root.ExecuteContext(context.Background())

			if tt.wantErr && err == nil {
				t.Fatalf("expected non-zero exit (error), got nil. stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected exit 0, got error %v. stderr=%q", err, stderr.String())
			}

			// The structured JSON body is always rendered.
			var got AuthAssertJSON
			if jErr := json.Unmarshal(stdout.Bytes(), &got); jErr != nil {
				t.Fatalf("decode JSON body: %v\nstdout=%q", jErr, stdout.String())
			}
			if got.OK != tt.wantOK {
				t.Errorf("rendered ok = %v, want %v", got.OK, tt.wantOK)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
			if strings.Contains(tt.name, "admin") && !gotAdmin {
				t.Errorf("--admin flag not threaded to assertIdentity (requireAdmin=false)")
			}
		})
	}
}

// TestAuthAssert_RequiresRepo verifies the verb errors when --repo is absent
// (parseRepo guards owner/name), before any identity work runs.
func TestAuthAssert_RequiresRepo(t *testing.T) {
	called := false
	orig := assertIdentity
	assertIdentity = func(_ context.Context, _, _ string, _ bool) (AuthAssertJSON, error) {
		called = true
		return AuthAssertJSON{}, nil
	}
	defer func() { assertIdentity = orig }()

	root := Cmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "assert", "--json"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error when --repo is missing")
	}
	if called {
		t.Error("assertIdentity must not be called when --repo is missing")
	}
}

func TestAuthRefresh_ReadsGhAndWrites(t *testing.T) {
	origRead := readGHToken
	origStore, origScrub := storeTokenInKeyring, scrubPlaintextCredentials
	readGHToken = func() (string, error) { return "ghp_refreshed_token_value_abcdef", nil }
	storeTokenInKeyring = func(token string) error { return nil }
	scrubPlaintextCredentials = func() ([]string, error) { return nil, nil }
	defer func() {
		readGHToken = origRead
		storeTokenInKeyring, scrubPlaintextCredentials = origStore, origScrub
	}()

	root := Cmd()
	stdout := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"auth", "refresh", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["refreshed"] != true {
		t.Errorf("refreshed should be true: %+v", got)
	}
	if strings.Contains(stdout.String(), "ghp_refreshed_token_value_abcdef") {
		t.Errorf("raw token leaked: %s", stdout.String())
	}
}
