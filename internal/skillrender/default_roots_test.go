package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeBundle builds the extension's packaging layout — <bundle>/dist/bin/nightgauge
// beside <bundle>/dist/skills/ — and returns the executable path.
func fakeBundle(t *testing.T, withSkills bool) string {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "dist", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "nightgauge")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withSkills {
		if err := os.MkdirAll(filepath.Join(root, "dist", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

// TestDefaultRootsAppendsTheBundleTree is the regression for #874.
//
// The Go-direct path (`queue run`, the autonomous scheduler) had exactly one
// root, `{workspaceRoot}/skills`, which exists in this repository and nowhere
// else. Every stage in every user's repo failed to render its SKILL.md while
// the file sat in the bundle one directory above the running binary.
func TestDefaultRootsAppendsTheBundleTree(t *testing.T) {
	exe := fakeBundle(t, true)
	bundleSkills := filepath.Join(filepath.Dir(filepath.Dir(exe)), "skills")

	roots := defaultRoots("/repo", exe)

	if len(roots) != 2 {
		t.Fatalf("roots = %v, want the workspace tree and the bundle tree", roots)
	}
	if roots[0] != filepath.Join("/repo", "skills") {
		t.Errorf("roots[0] = %q, want the workspace tree first", roots[0])
	}
	// EvalSymlinks: macOS /var/folders temp dirs resolve through /private.
	wantBundle, err := filepath.EvalSymlinks(bundleSkills)
	if err != nil {
		wantBundle = bundleSkills
	}
	if roots[1] != wantBundle {
		t.Errorf("roots[1] = %q, want the bundle tree %q", roots[1], wantBundle)
	}
}

// TestDefaultRootsOmitsAMissingBundleTree answers DefaultRoots's own objection:
// a root that cannot match must never be returned, so a plain `go build` binary
// with no bundle beside it keeps exactly one root.
func TestDefaultRootsOmitsAMissingBundleTree(t *testing.T) {
	exe := fakeBundle(t, false)

	roots := defaultRoots("/repo", exe)

	if len(roots) != 1 {
		t.Fatalf("roots = %v, want only the workspace tree when no bundle exists", roots)
	}
}

// TestDefaultRootsSurvivesAnUnknownExecutable pins the degenerate arm:
// os.Executable can fail, and a render must still search the workspace.
func TestDefaultRootsSurvivesAnUnknownExecutable(t *testing.T) {
	roots := defaultRoots("/repo", "")

	if len(roots) != 1 || roots[0] != filepath.Join("/repo", "skills") {
		t.Fatalf("roots = %v, want just the workspace tree", roots)
	}
}

// TestGoAndTypeScriptAgreeOnSkillRootOrder is the test that would have caught
// #874, and the reason it is written across both languages.
//
// The two hosts resolve skill roots independently: this package for the
// Go-direct path, resolveSkillRoots() in skillRunner.ts for the extension.
// Asserting Go's roots ALONE passed throughout the bug's life — Go was
// self-consistent and simply searched one place. Only a comparison against the
// other implementation shows the disagreement, so this reads the TypeScript
// source and pins the ORDER both must use: workspace checkout first, extension
// bundle second.
func TestGoAndTypeScriptAgreeOnSkillRootOrder(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(
		"..", "..", "packages", "nightgauge-vscode", "src", "utils", "skillRunner.ts"))
	if err != nil {
		t.Fatalf("read skillRunner.ts: %v", err)
	}

	body := between(t, string(src), "export function resolveSkillRoots()", "\n}")

	pushes := regexp.MustCompile(`roots\.push\(([^)]*)\)`).FindAllStringSubmatch(body, -1)
	if len(pushes) != 2 {
		t.Fatalf("resolveSkillRoots pushes %d roots, want 2 — if the host's root "+
			"list changed, DefaultRoots must change with it (#874)", len(pushes))
	}
	if !strings.Contains(pushes[0][1], "workspaceRoot") {
		t.Errorf("TS root 0 = %q, want the workspace checkout first", pushes[0][1])
	}
	if !strings.Contains(pushes[1][1], "bundleRoot") {
		t.Errorf("TS root 1 = %q, want the extension bundle second", pushes[1][1])
	}

	// Go must produce the same two, in the same order.
	exe := fakeBundle(t, true)
	roots := defaultRoots("/repo", exe)
	if len(roots) != 2 {
		t.Fatalf("Go roots = %v, want 2 to match the TypeScript host", roots)
	}
	if !strings.HasSuffix(roots[0], filepath.Join("repo", "skills")) {
		t.Errorf("Go root 0 = %q, want the workspace checkout first", roots[0])
	}
	if !strings.Contains(roots[1], filepath.Join("dist", "skills")) {
		t.Errorf("Go root 1 = %q, want the extension bundle second", roots[1])
	}
}

func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("marker %q not found — did resolveSkillRoots move or get renamed?", start)
	}
	rest := s[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("end marker %q not found after %q", end, start)
	}
	return rest[:j]
}
