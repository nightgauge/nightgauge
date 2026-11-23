package workspacemanifest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// writeManifestAt writes a manifest listing paths at dir and returns dir.
func writeManifestAt(t *testing.T, dir string, paths ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("version: 1\nrepositories:\n")
	for i, p := range paths {
		b.WriteString("  - name: r" + string(rune('a'+i)) + "\n    path: " + p + "\n    role: primary\n")
	}
	if err := os.WriteFile(ManifestPath(dir), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// realTemp returns a canonical temp dir (macOS /var is a symlink to /private/var).
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// withOrigin makes dir a git repository whose origin is url ("" = none).
func withOrigin(t *testing.T, dir, url string) string {
	t.Helper()
	gittest.InitRepo(t, dir, "-q")
	if url != "" {
		gittest.Run(t, dir, "remote", "add", "origin", url)
	}
	return dir
}

// isolateMachineConfig points the machine tier at a temp dir holding body.
func isolateMachineConfig(t *testing.T, body string) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", d)
	t.Setenv("XDG_CONFIG_HOME", "")
	if body != "" {
		if err := os.WriteFile(filepath.Join(d, "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// captureRefusals swaps the package-level refusal writer and dedupe map, so
// tests that use it must not run in parallel.
func captureRefusals(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := refusalLog
	refusalLog = &buf
	refusedClaims.Clear()
	t.Cleanup(func() { refusalLog = prev; refusedClaims.Clear() })
	return &buf
}

func TestRootOf(t *testing.T) {
	isolateMachineConfig(t, "")
	base := realTemp(t)
	a := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "a")), "https://github.com/acme/a.git"), ".", "../b", "nested/c")
	b := withOrigin(t, mkdir(t, filepath.Join(base, "b")), "git@github.com:acme/b.git")
	c := mkdir(t, filepath.Join(a, "nested", "c"))
	unlisted := mkdir(t, filepath.Join(base, "unlisted"))

	cases := []struct {
		name, in, want string
	}{
		{"checkout holding the manifest", a, a},
		{"listed sibling", b, a},
		{"listed descendant", c, a},
		{"unlisted sibling is its own workspace", unlisted, unlisted},
	}
	for _, tc := range cases {
		if got := RootOf(tc.in); got != tc.want {
			t.Errorf("%s: RootOf(%s) = %s, want %s", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRootOf_UnlistedDescendantIsItsOwnWorkspace(t *testing.T) {
	base := realTemp(t)
	a := writeManifestAt(t, mkdir(t, filepath.Join(base, "a")), ".")
	inner := mkdir(t, filepath.Join(a, "vendor", "x"))
	if got := RootOf(inner); got != inner {
		t.Errorf("RootOf(%s) = %s, want %s: a manifest that does not list a descendant does not claim it",
			inner, got, inner)
	}
}

func TestRootOf_InvalidManifestIgnored(t *testing.T) {
	base := realTemp(t)
	a := mkdir(t, filepath.Join(base, "a"))
	b := mkdir(t, filepath.Join(base, "b"))
	mkdir(t, filepath.Join(a, ".vscode"))
	if err := os.WriteFile(ManifestPath(a), []byte("repositories: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RootOf(b); got != b {
		t.Errorf("RootOf(%s) = %s, want %s: an unparsable sibling manifest must not claim it", b, got, b)
	}
	if got := RootOf(a); got != a {
		t.Errorf("RootOf(%s) = %s, want %s", a, got, a)
	}
}

func TestRootOf_SymlinkCanonicalises(t *testing.T) {
	isolateMachineConfig(t, "")
	base := realTemp(t)
	a := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "a")), "https://github.com/acme/a"), ".", "../b")
	b := withOrigin(t, mkdir(t, filepath.Join(base, "b")), "https://github.com/acme/b")
	link := filepath.Join(realTemp(t), "link-to-b")
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if got := RootOf(link); got != a {
		t.Errorf("RootOf(%s) = %s, want %s", link, got, a)
	}
}

// Rule 1 before rule 2: an ancestor manifest that lists the checkout wins
// over a sibling manifest that lists it too.
func TestRootOf_AncestorBeatsSibling(t *testing.T) {
	base := realTemp(t)
	ancestor := writeManifestAt(t, mkdir(t, filepath.Join(base, "ws")), "sub/repo")
	repo := mkdir(t, filepath.Join(ancestor, "sub", "repo"))
	writeManifestAt(t, mkdir(t, filepath.Join(ancestor, "sub", "a-sibling")), "../repo")
	if got := RootOf(repo); got != ancestor {
		t.Fatalf("RootOf(%s) = %s, want the ancestor manifest root %s", repo, got, ancestor)
	}
}

// Two sibling manifests list the same checkout: the first in name order wins.
func TestRootOf_SiblingsResolveInNameOrder(t *testing.T) {
	isolateMachineConfig(t, "")
	base := realTemp(t)
	first := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "a-ws")), "https://github.com/acme/ws"), "../repo")
	writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "z-ws")), "https://github.com/acme/ws2"), "../repo")
	repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), "https://github.com/acme/repo")
	if got := RootOf(repo); got != first {
		t.Fatalf("RootOf(%s) = %s, want the first sibling in name order %s", repo, got, first)
	}
}

// #3495: an untrusted sibling cannot move a checkout's workspace root.
func TestRootOf_SiblingClaimTrust(t *testing.T) {
	cases := []struct {
		name, rootURL, repoURL, machine string
		trustRoot, trusted              bool
	}{
		{"different owners refused", "https://github.com/evil/ws", "https://github.com/acme/repo", "", false, false},
		{"different hosts refused", "https://gitlab.com/acme/ws", "https://github.com/acme/repo", "", false, false},
		{"same owner across forms and case", "ssh://git@GitHub.com:22/Acme/ws.git", "https://github.com/acme/repo", "", false, true},
		{"no origin on checkout refused", "https://github.com/acme/ws", "", "", false, false},
		{"no origin on root refused", "", "https://github.com/acme/repo", "", false, false},
		{"trusted_roots overrides owners", "https://github.com/evil/ws", "https://github.com/acme/repo", "", true, true},
		{"trusted_roots overrides no origin", "", "", "", true, true},
		{"relative trusted_roots ignored", "", "", "workspace:\n  trusted_roots: [ws]\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := realTemp(t)
			ws := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "ws")), tc.rootURL), "../repo")
			repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), tc.repoURL)
			machine := tc.machine
			if tc.trustRoot {
				machine = "workspace:\n  trusted_roots: [" + ws + "]\n"
			}
			isolateMachineConfig(t, machine)
			buf := captureRefusals(t)
			want := repo
			if tc.trusted {
				want = ws
			}
			if got := RootOf(repo); got != want {
				t.Fatalf("RootOf(%s) = %s, want %s", repo, got, want)
			}
			if logged := buf.Len() > 0; logged == tc.trusted {
				t.Fatalf("refusal logged = %v for trusted = %v: %q", logged, tc.trusted, buf.String())
			}
		})
	}
}

func TestRootOf_RefusalLoggedOnceAndFallsThrough(t *testing.T) {
	isolateMachineConfig(t, "")
	buf := captureRefusals(t)
	base := realTemp(t)
	evil := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "a-evil")), "https://github.com/evil/x"), "../repo")
	good := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "b-ws")), "https://github.com/acme/ws"), "../repo")
	repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), "git@github.com:acme/repo.git")
	for range 2 {
		if got := RootOf(repo); got != good {
			t.Fatalf("RootOf(%s) = %s, want trusted later sibling %s", repo, got, good)
		}
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, evil) || !strings.Contains(out, repo) {
		t.Fatalf("refusal log = %q, want one line naming %s and %s", out, evil, repo)
	}
}

func TestParseRemoteOwner(t *testing.T) {
	cases := []struct {
		in, host, owner string
		ok              bool
	}{
		{"https://github.com/Acme/repo.git", "github.com", "acme", true},
		{"http://host.example/o/r", "host.example", "o", true},
		{"ssh://git@github.com/acme/repo", "github.com", "acme", true},
		{"ssh://git@github.com:2222/acme/repo", "github.com", "acme", true},
		{"git@github.com:acme/repo.git", "github.com", "acme", true},
		{"https://github.com/acme", "", "", false},
		{"/local/path/repo", "", "", false},
		{"", "", "", false},
		{"https://github.com", "", "", false},
		{"https://github.com:x@evil.example/acme/r", "evil.example", "acme", true},
		{"https://[2001:db8::1]/acme/r", "", "", false},
		{"file:///srv/acme/r", "", "", false},
		{"/tmp/a@b:c/d", "", "", false},
		{"x:y@z", "", "", false},
		{"alias:org/user@repo", "", "", false},
	}
	for _, c := range cases {
		h, o, ok := parseRemoteOwner(c.in)
		if h != c.host || o != c.owner || ok != c.ok {
			t.Errorf("parseRemoteOwner(%q) = %q,%q,%v want %q,%q,%v", c.in, h, o, ok, c.host, c.owner, c.ok)
		}
	}
}

// trusted_roots counts only in the machine file: a committed project config
// in the claiming sibling or the checkout cannot trust the sibling.
func TestRootOf_CommittedTrustedRootsIgnored(t *testing.T) {
	isolateMachineConfig(t, "")
	captureRefusals(t)
	base := realTemp(t)
	ws := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "ws")), "https://github.com/evil/ws"), "../repo")
	repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), "https://github.com/acme/repo")
	for _, d := range []string{ws, repo} {
		mkdir(t, filepath.Join(d, ".nightgauge"))
		body := "workspace:\n  trusted_roots: [" + ws + "]\n"
		if err := os.WriteFile(filepath.Join(d, ".nightgauge", "config.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := RootOf(repo); got != repo {
		t.Fatalf("RootOf(%s) = %s, want %s: a committed config must not grant trust", repo, got, repo)
	}
}

// A trusted root written through a symlink still matches; an unparseable
// machine file grants nothing.
func TestRootOf_TrustedRootsCanonicalisedAndUnparseableIgnored(t *testing.T) {
	base := realTemp(t)
	ws := writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "ws")), ""), "../repo")
	repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), "")
	link := filepath.Join(realTemp(t), "ws-link")
	if err := os.Symlink(ws, link); err != nil {
		t.Fatal(err)
	}
	captureRefusals(t)
	isolateMachineConfig(t, "workspace:\n  trusted_roots: ["+link+"]\n")
	if got := RootOf(repo); got != ws {
		t.Fatalf("RootOf(%s) = %s, want %s via symlinked trusted root", repo, got, ws)
	}
	isolateMachineConfig(t, "workspace: [unclosed\n")
	if got := RootOf(repo); got != repo {
		t.Fatalf("RootOf(%s) = %s, want %s with an unparseable machine file", repo, got, repo)
	}
}

// Dedupe is per pair: a second checkout refused by the same root gets its own line.
func TestRootOf_RefusalDedupedPerPair(t *testing.T) {
	isolateMachineConfig(t, "")
	buf := captureRefusals(t)
	base := realTemp(t)
	writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "ws")), "https://github.com/evil/ws"), "../r1", "../r2")
	r1 := withOrigin(t, mkdir(t, filepath.Join(base, "r1")), "https://github.com/acme/r1")
	r2 := withOrigin(t, mkdir(t, filepath.Join(base, "r2")), "https://github.com/acme/r2")
	RootOf(r1)
	RootOf(r2)
	RootOf(r1)
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("refusal lines = %d, want 2: %q", n, buf.String())
	}
}

// An inherited GIT_DIR (as in a hook) must not make every directory report
// the same origin.
func TestRootOf_InheritedGitDirIgnored(t *testing.T) {
	isolateMachineConfig(t, "")
	captureRefusals(t)
	base := realTemp(t)
	writeManifestAt(t, withOrigin(t, mkdir(t, filepath.Join(base, "ws")), "https://github.com/evil/ws"), "../repo")
	repo := withOrigin(t, mkdir(t, filepath.Join(base, "repo")), "https://github.com/acme/repo")
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	if got := RootOf(repo); got != repo {
		t.Fatalf("RootOf(%s) = %s, want %s with GIT_DIR inherited", repo, got, repo)
	}
}
