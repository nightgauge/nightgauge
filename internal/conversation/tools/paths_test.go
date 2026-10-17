package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func unixMkfifo(path string) error { return unix.Mkfifo(path, 0o644) }

// commitAll tracks everything in root that is not ignored.
func commitAll(t *testing.T, root string) {
	t.Helper()
	git(t, root, "add", "-A")
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "more")
}

// File reads are contained to the workspace root (#1570): a path that climbs
// out with "..", an absolute path, and a symlink in the workspace that
// resolves outside it are all refused, by every file tool, and the outside
// file's content never reaches a result. The symlinks are tracked, so the
// containment is what refuses them, not the tracked-files rule; a turn is
// told only that nothing it may read is there, since the containment's own
// refusal would say the target exists. Removing the containment makes every
// case red.
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
	commitAll(t, root)
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
	for _, p := range []string{"../" + filepath.Base(outside) + "/secret.txt", "src/../../x", "/etc/passwd"} {
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); code(err) != CodeOutsideWorkspace {
			t.Errorf("read_file %q: %v, want %s", p, err, CodeOutsideWorkspace)
		}
	}
	for _, p := range []string{"escape", "escape-dir", "src/relative-escape"} {
		if _, err := s.fs.resolve(p); code(err) != CodeOutsideWorkspace {
			t.Errorf("resolve %q: %v, want %s", p, err, CodeOutsideWorkspace)
		}
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); err == nil || *err != *errNotServed {
			t.Errorf("read_file %q: %v, want the refusal a missing file gets", p, err)
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
// of a tracked .nightgauge config file are redacted and the rest of the file
// is read. Removing the deny list makes it red.
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
	// Tracked, every one, so the deny list is what refuses them: a secret
	// is refused even when it is committed.
	commitAll(t, root)
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))

	for _, p := range []string{".env", ".env.local", "deploy/server.pem", "deploy/tls.KEY", ".git/config",
		".git/HEAD", ".GIT/config", ".Env", "src/../.env"} {
		if _, err := call(t, s, ToolReadFile, map[string]any{"path": p}); code(err) != CodeSecretPath {
			t.Errorf("read_file %q: %v, want %s", p, err, CodeSecretPath)
		}
	}
	// A tracked symlink to a secret is refused by the deny list too; the
	// turn is told only that nothing it may read is there.
	if _, err := s.fs.resolve("notes.txt"); code(err) != CodeSecretPath {
		t.Errorf("resolve notes.txt, a symlink to .env: %v, want %s", err, CodeSecretPath)
	}
	if out, err := call(t, s, ToolReadFile, map[string]any{"path": "notes.txt"}); err == nil || *err != *errNotServed ||
		strings.Contains(out, "env-secret-value") {
		t.Errorf("read_file notes.txt, a symlink to .env: %v %s", err, out)
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
	if err != nil || strings.Contains(out, "local-secret-token") || !strings.Contains(out, redactedValue) {
		t.Errorf("the tracked local config showed its token: %v %s", err, out)
	}
}

// A config file that does not parse is not shown at all: its credentials
// cannot be found to redact.
func TestRedactConfig_RefusesWhatItCannotParse(t *testing.T) {
	if _, err := redactConfig([]byte("github_auth: [unclosed\n  token: ghp_x")); code(err) != CodeSecretPath {
		t.Errorf("unparseable config: %v, want %s", err, CodeSecretPath)
	}
}

// Security review, finding 1 (validator differential): the deny list
// compared spellings with strings.ToLower, but a case-insensitive filesystem
// compares by Unicode case folding, and some ignore invisible characters, so
// a spelling the list did not match could open the secret. ToLower leaves
// the long s (ſ) alone, which APFS folds to s, so .git-credentialſ opened
// .git-credentials; a zero-width character inside .env or .git went
// unmatched; a trailing space or dot was not trimmed. Every spelling a
// filesystem may treat as the secret's is now refused before anything is
// opened, whether or not that filesystem would open it.
func TestSecretPaths_FoldedSpellingsAreRefused(t *testing.T) {
	root := gitRepo(t)
	write(t, root, ".git-credentials", "https://user:credential-secret@example.com\n")
	write(t, root, "id_rsa", "ssh-secret\n")
	write(t, root, ".env", "API_KEY=env-secret\n")
	write(t, root, "deploy/server.key", "key-secret\n")
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))
	for _, p := range []string{
		".git-credentialſ", // long s: APFS folds it to s
		"id_rſa",
		"ID_RSA",
		"deploy/server.Key", // Kelvin sign
		".e​nv",             // zero-width space
		".⁠env",             // word joiner
		".gi‍t/config",      // zero-width joiner in a directory component
		"id_rsa ",           // trailing space
		"id_rsa.",           // trailing dot
		"deploy/server.key.",
		".GIT️/HEAD", // variation selector
	} {
		out, err := call(t, s, ToolReadFile, map[string]any{"path": p})
		if code(err) != CodeSecretPath {
			t.Errorf("read_file %q: %v %s, want %s", p, err, out, CodeSecretPath)
		}
		for _, secret := range []string{"credential-secret", "ssh-secret", "env-secret", "key-secret"} {
			if strings.Contains(out, secret) {
				t.Errorf("read_file %q returned %s", p, secret)
			}
		}
	}
	// A listing never names them, under any spelling the directory holds,
	// tracked though they are.
	write(t, root, "keys/ID_RSA", "x\n")
	write(t, root, "keys/.Git-Credentials", "x\n")
	write(t, root, "keys/README.md", "x\n")
	commitAll(t, root)
	out, err := call(t, s, ToolListDirectory, map[string]any{"path": "keys"})
	if err != nil || strings.Contains(out, "ID_RSA") || strings.Contains(out, "Credentials") {
		t.Errorf("listing named a secret: %v %s", err, out)
	}
}

// Security review, finding 1 (validator differential): the path was checked
// once, resolved through its symlinks, and then opened again by name with an
// os.Root, which follows any symlink inside the root. A component swapped for
// a symlink between the check and the open was followed to a file the check
// never saw. The open now walks the checked path one component at a time
// with O_NOFOLLOW, so a symlink that appears anywhere on it fails the open.
func TestOpen_NeverFollowsASymlinkTheCheckDidNotSee(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "sub/notes.txt", "benign\n")
	write(t, root, ".git/notes.txt", "GIT-INTERNAL-SECRET\n")
	write(t, root, "other/notes.txt", "benign\n")
	write(t, root, ".env", "ENV-SECRET\n")
	write(t, root, "dir/a.txt", "benign\n")
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))

	// A directory on the path becomes a symlink to .git.
	rel, rerr := s.fs.resolve("sub/notes.txt")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "sub-was")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if text, err := s.readText(rel); err == nil || strings.Contains(text, "GIT-INTERNAL-SECRET") {
		t.Errorf("a directory swapped for a symlink was followed: %v %q", err, text)
	}

	// The file itself becomes a symlink to .env.
	rel, rerr = s.fs.resolve("other/notes.txt")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := os.Remove(filepath.Join(root, "other", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", ".env"), filepath.Join(root, "other", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if text, err := s.readText(rel); err == nil || strings.Contains(text, "ENV-SECRET") {
		t.Errorf("a file swapped for a symlink was followed: %v %q", err, text)
	}

	// A directory to list becomes a symlink to .git.
	rel, rerr = s.fs.resolve("dir")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "dir-was")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if f, err := s.fs.open(rel); err == nil {
		f.Close()
		t.Error("a directory swapped for a symlink was opened")
	}

	// The open refuses a path that was never checked.
	if f, err := s.fs.open(".git/notes.txt"); err == nil {
		f.Close()
		t.Error("open accepted a secret path it was handed directly")
	}
}

// A FIFO in the workspace does not hang a read. git does not track one, so
// it stands where a tracked file was.
func TestReadFile_AFIFODoesNotBlock(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "pipe", "tracked\n")
	commitAll(t, root)
	if err := os.Remove(filepath.Join(root, "pipe")); err != nil {
		t.Fatal(err)
	}
	if err := unixMkfifo(filepath.Join(root, "pipe")); err != nil {
		t.Skip("mkfifo:", err)
	}
	s := toolset(t, root, newFakeForge(), operatorTurn("read"))
	done := make(chan *Error, 1)
	go func() {
		_, err := call(t, s, ToolReadFile, map[string]any{"path": "pipe"})
		done <- err
	}()
	select {
	case err := <-done:
		if code(err) != CodeNotText {
			t.Errorf("read_file on a FIFO: %v, want %s", err, CodeNotText)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read_file blocked on a FIFO")
	}
}
