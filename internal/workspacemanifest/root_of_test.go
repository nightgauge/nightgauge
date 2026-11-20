package workspacemanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRootOf(t *testing.T) {
	base := realTemp(t)
	a := writeManifestAt(t, mkdir(t, filepath.Join(base, "a")), ".", "../b", "nested/c")
	b := mkdir(t, filepath.Join(base, "b"))
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
	base := realTemp(t)
	a := writeManifestAt(t, mkdir(t, filepath.Join(base, "a")), ".", "../b")
	b := mkdir(t, filepath.Join(base, "b"))
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
	base := realTemp(t)
	first := writeManifestAt(t, mkdir(t, filepath.Join(base, "a-ws")), "../repo")
	writeManifestAt(t, mkdir(t, filepath.Join(base, "z-ws")), "../repo")
	repo := mkdir(t, filepath.Join(base, "repo"))
	if got := RootOf(repo); got != first {
		t.Fatalf("RootOf(%s) = %s, want the first sibling in name order %s", repo, got, first)
	}
}
