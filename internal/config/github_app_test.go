package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testAppPEM stands in for key material. The config layer only reads the
// bytes; parsing them is internal/github's job and is tested there against a
// key generated at run time, so no key-shaped literal lives in the tree.
const testAppPEM = "test-app-key-material-1955\n"

// #1955: the App block decodes through the real loader, `id` written as a
// YAML integer included, and resolves per owner.
func TestResolveGitHubApp_FromYAML(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(keyPath, []byte(testAppPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "owner: nightgauge\ngithub_auth:\n  app:\n    id: 123456\n    private_key_path: " + keyPath +
		"\n    installations:\n      nightgauge: 42\n    slug: nightgauge-pipeline\n    bot_user_id: 99\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	creds, err := cfg.ResolveGitHubApp("nightgauge")
	if err != nil || creds == nil {
		t.Fatalf("creds=%v err=%v", creds, err)
	}
	if creds.AppID != "123456" || creds.InstallationID != 42 || string(creds.PrivateKeyPEM) != testAppPEM || creds.BotUserID != 99 {
		t.Errorf("creds = %+v", creds)
	}
	if other, err := cfg.ResolveGitHubApp("someone-else"); other != nil || err != nil {
		t.Errorf("an owner without an installation keeps the personal chain: %v %v", other, err)
	}
}

func TestResolveGitHubApp_KeyFromEnv(t *testing.T) {
	t.Setenv("TEST_APP_KEY_1955", testAppPEM)
	cfg := &Config{GitHubAuth: &GitHubAuthConfig{App: &GitHubAppConfig{
		ID: "7", PrivateKey: "env:TEST_APP_KEY_1955", Installations: map[string]int64{"nightgauge": 1},
	}}}
	creds, err := cfg.ResolveGitHubApp("NightGauge")
	if err != nil || creds == nil || string(creds.PrivateKeyPEM) != testAppPEM {
		t.Fatalf("creds=%v err=%v", creds, err)
	}
}

// A configured but unusable App is an error, never a silent "no App", and the
// error never carries the key.
func TestResolveGitHubApp_BrokenConfigurationIsAnErrorWithoutTheKey(t *testing.T) {
	for name, app := range map[string]*GitHubAppConfig{
		"plaintext key": {ID: "7", PrivateKey: testAppPEM},
		"missing file":  {ID: "7", PrivateKeyPath: filepath.Join(t.TempDir(), "absent.pem")},
		"no key at all": {ID: "7"},
		"no app id":     {PrivateKeyPath: "/dev/null"},
		"unset env var": {ID: "7", PrivateKey: "env:TEST_APP_KEY_UNSET_1955"},
	} {
		app.Installations = map[string]int64{"nightgauge": 1}
		cfg := &Config{GitHubAuth: &GitHubAuthConfig{App: app}}
		creds, err := cfg.ResolveGitHubApp("nightgauge")
		if err == nil || creds != nil {
			t.Errorf("%s: creds=%v err=%v, want an error", name, creds, err)
			continue
		}
		if strings.Contains(err.Error(), "test-app-key-material") {
			t.Errorf("%s: error quotes the key: %v", name, err)
		}
	}
}

// #1955 AC3: the key is machine-tier. A repository tier may only name it
// through env:.
func TestValidateRepoTierSecrets_RefusesAnAppKeyInARepository(t *testing.T) {
	for _, body := range []string{
		"github_auth:\n  app:\n    private_key_path: ./github-app.pem\n",
		"github_auth:\n  app:\n    private_key: literal-key-material\n",
	} {
		if err := ValidateRepoTierSecrets([]byte(body), "/repo/.nightgauge/config.yaml"); err == nil {
			t.Errorf("accepted in a repository tier:\n%s", body)
		}
	}
	ok := "github_auth:\n  app:\n    id: 1\n    private_key: env:APP_KEY\n    installations: {nightgauge: 1}\n"
	if err := ValidateRepoTierSecrets([]byte(ok), "/repo/.nightgauge/config.yaml"); err != nil {
		t.Errorf("an env: reference must be accepted: %v", err)
	}
}
