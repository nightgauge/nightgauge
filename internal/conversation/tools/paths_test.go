package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// File reads are contained to the workspace root (#1570): a path that climbs
// out with "..", an absolute path, and a symlink planted in the workspace
// that resolves outside it are all refused, by every file tool, and the
// outside file's content never reaches a result. Removing the containment
// makes every case red.
func TestContainment_RefusesTraversalAndEscapingSymlinks(t *testing.T) {
	root := gitRepo(t)
	outside := t.TempDir()
	write(t, outside, "secret.txt", "OUTSIDE-CONTENT\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../"+filepath.Base(outside), filepath.Join(root, "src", "relative-escape")); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside is followed.
	if err := os.Symlink("README.md", filepath.Join(root, "readme-link")); err != nil {
		t.Fatal(err)
	}
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))
	rel, _ := filepath.Rel(root, filepath.Join(outside, "secret.txt"))

	for _, p := range []string{
		"../" + filepath.Base(outside) + "/secret.txt", rel, "src/../../x", filepath.Join(outside, "secret.txt"),
		"/etc/passwd", "escape", "escape-dir/secret.txt", "escape-dir", "src/relative-escape/secret.txt",
	} {
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); code(err) != CodeOutsideWorkspace && code(err) != CodeNotFound {
			t.Errorf("read_file %q: %v, want refused as outside the workspace", p, err)
		}
		if _, err := call(t, s, ToolListDirectory, map[string]any{"path": p}); err == nil {
			t.Errorf("list_directory %q was answered", p)
		}
		if out, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": "OUTSIDE", "path": p}); err == nil &&
			strings.Contains(out, "OUTSIDE-CONTENT") {
			t.Errorf("search_files %q reached outside: %s", p, out)
		}
	}
	for _, p := range []string{"escape", "escape-dir/secret.txt", "../" + filepath.Base(outside) + "/secret.txt"} {
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); code(err) != CodeOutsideWorkspace {
			t.Errorf("read_file %q: %v, want %s", p, err, CodeOutsideWorkspace)
		}
	}
	// A search of the whole root never follows the planted symlinks out.
	out, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": "OUTSIDE"})
	if err != nil || strings.Contains(out, "OUTSIDE-CONTENT") {
		t.Errorf("a root search reached outside the workspace: %v %s", err, out)
	}
	out, err = call(t, s, ToolReadFile, map[string]any{"path": "readme-link"})
	if err != nil || !strings.Contains(out, "The API service.") {
		t.Errorf("a symlink inside the workspace was not followed: %v %s", err, out)
	}
}

// Inside the root, secret-bearing paths are refused (#1570): .env*, *.pem,
// *.key, .git internals, whether named directly, in another case, or reached
// through a symlink, and a listing never names them. The credential fields
// of .nightgauge/config.yaml are redacted and the rest of the file is read.
// Removing the deny list makes it red.
func TestSecretPaths_AreRefusedAndConfigCredentialsRedacted(t *testing.T) {
	root := gitRepo(t)
	write(t, root, ".env", "API_KEY=env-secret-value\n")
	write(t, root, ".env.local", "API_KEY=env-local-secret\n")
	write(t, root, "deploy/server.pem", "pem-secret-value\n")
	write(t, root, "deploy/tls.KEY", "key-secret-value\n")
	write(t, root, ".nightgauge/config.local.yaml", "github_auth:\n  tokens:\n    acme: local-secret-token\n")
	write(t, root, ".nightgauge/config.yaml", "owner: acme\ndefault_repo: api\nplatform:\n  license_key: license-secret-value\n"+
		"github_auth:\n  token: config-secret-token\n  tokens:\n    acme: org-secret-token\n  app:\n    id: \"123\"\n"+
		"    private_key_path: ~/.nightgauge/app.pem\n")
	if err := os.Symlink(".env", filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))

	for _, p := range []string{".env", ".env.local", "deploy/server.pem", "deploy/tls.KEY", ".git/config",
		".git/HEAD", ".GIT/config", ".Env", "notes.txt", "src/../.env"} {
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); code(err) != CodeSecretPath {
			t.Errorf("read_file %q: %v, want %s", p, err, CodeSecretPath)
		}
	}
	if _, err := call(t, s, ToolListDirectory, map[string]any{"path": ".git"}); code(err) != CodeSecretPath {
		t.Errorf("list_directory .git: %v", err)
	}
	out, err := call(t, s, ToolListDirectory, map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{`".env"`, `".env.local"`, `".git"`} {
		if strings.Contains(out, hidden) {
			t.Errorf("the listing names %s: %s", hidden, out)
		}
	}
	for _, needle := range []string{"secret-value", "secret-token", "env-local-secret", "app.pem"} {
		out, err := call(t, s, ToolSearchFiles, map[string]any{"pattern": needle})
		if err != nil {
			t.Fatalf("search for %q: %v", needle, err)
		}
		if _, content := envelope(t, out); len(content["matches"].([]any)) != 0 {
			t.Errorf("search for %q found a secret: %v", needle, content["matches"])
		}
	}

	out, err = call(t, s, ToolReadFile, map[string]any{"path": ".nightgauge/config.yaml"})
	if err != nil {
		t.Fatalf("the config file is readable with its credentials redacted: %v", err)
	}
	_, content := envelope(t, out)
	text := content["text"].(string)
	for _, secret := range []string{"config-secret-token", "org-secret-token", "license-secret-value", "app.pem"} {
		if strings.Contains(text, secret) {
			t.Errorf("config read shows %q:\n%s", secret, text)
		}
	}
	for _, kept := range []string{"owner: acme", "default_repo: api", `id: "123"`, redactedValue} {
		if !strings.Contains(text, kept) {
			t.Errorf("config read lost %q:\n%s", kept, text)
		}
	}
	out, err = call(t, s, ToolReadFile, map[string]any{"path": ".nightgauge/config.local.yaml"})
	if err != nil || strings.Contains(out, "local-secret-token") {
		t.Errorf("the local config showed its token: %v %s", err, out)
	}
}

// A config file that does not parse is not shown at all: its credentials
// cannot be found to redact.
func TestRedactConfig_RefusesWhatItCannotParse(t *testing.T) {
	if _, err := redactConfig([]byte("github_auth: [unclosed\n  token: ghp_x")); code(err) != CodeSecretPath {
		t.Errorf("unparseable config: %v, want %s", err, CodeSecretPath)
	}
}
