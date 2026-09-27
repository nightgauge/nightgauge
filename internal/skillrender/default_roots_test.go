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

	roots := defaultRoots("/repo", exe, "")

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

	roots := defaultRoots("/repo", exe, "")

	if len(roots) != 1 {
		t.Fatalf("roots = %v, want only the workspace tree when no bundle exists", roots)
	}
}

// TestDefaultRootsSurvivesAnUnknownExecutable pins the degenerate arm:
// os.Executable can fail, and a render must still search the workspace.
func TestDefaultRootsSurvivesAnUnknownExecutable(t *testing.T) {
	roots := defaultRoots("/repo", "", "")

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
	roots := defaultRoots("/repo", exe, "")
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

// TestDefaultRootsOrderWithOverride pins #2220's order: the workspace tree
// first, the operator's NIGHTGAUGE_SKILLS_ROOT second, the bundle last.
func TestDefaultRootsOrderWithOverride(t *testing.T) {
	exe := fakeBundle(t, true)
	override := t.TempDir()

	roots := defaultRoots("/repo", exe, override)

	if len(roots) != 3 {
		t.Fatalf("roots = %v, want workspace, override, bundle", roots)
	}
	if roots[0] != filepath.Join("/repo", "skills") {
		t.Errorf("roots[0] = %q, want the workspace tree", roots[0])
	}
	if roots[1] != override {
		t.Errorf("roots[1] = %q, want the override %q", roots[1], override)
	}
	if !strings.HasSuffix(roots[2], filepath.Join("dist", "skills")) {
		t.Errorf("roots[2] = %q, want the bundle tree", roots[2])
	}
}

// TestDefaultRootsKeepsAMissingOverride: an explicit override is listed even
// when it does not exist, so a typo shows in the not-found error.
func TestDefaultRootsKeepsAMissingOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	roots := defaultRoots("/repo", "", "  "+missing+" ")
	if len(roots) != 2 || roots[1] != missing {
		t.Fatalf("roots = %v, want the workspace tree and %q", roots, missing)
	}
	if got := defaultRoots("/repo", "", "   "); len(got) != 1 {
		t.Fatalf("blank override: roots = %v, want only the workspace tree", got)
	}
}

// TestDefaultRootsReadsTheEnvOverride proves the exported entry point reads
// NIGHTGAUGE_SKILLS_ROOT and that Locate resolves a stage through it — the
// exact failure #2220 reported from a repository without skills/.
func TestDefaultRootsReadsTheEnvOverride(t *testing.T) {
	override := t.TempDir()
	skillDir := filepath.Join(override, StageSkillDirs["issue-pickup"])
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# pickup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SkillsRootEnv, override)

	workspace := t.TempDir() // a repository with no skills/ tree
	roots := DefaultRoots(workspace)
	if len(roots) < 2 || roots[1] != override {
		t.Fatalf("DefaultRoots = %v, want %q second", roots, override)
	}
	path, err := Locate("issue-pickup", roots)
	if err != nil {
		t.Fatalf("Locate through %s: %v", SkillsRootEnv, err)
	}
	if !strings.HasPrefix(path, override) && !strings.Contains(path, filepath.Base(override)) {
		t.Errorf("path = %q, want it under the override %q", path, override)
	}
}

// TestLocateErrorNamesRootsAndFixes pins the not-found text: every searched
// root, and both remedies (bundle layout, NIGHTGAUGE_SKILLS_ROOT).
func TestLocateErrorNamesRootsAndFixes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	_, err := Locate("issue-pickup", []string{a, b})
	if err == nil {
		t.Fatal("Locate succeeded over empty roots")
	}
	msg := err.Error()
	for _, want := range []string{
		`SKILL.md not found for stage "issue-pickup"`,
		"searched roots " + a + ", " + b,
		"<prefix>/bin/nightgauge beside <prefix>/skills/",
		SkillsRootEnv,
		"nightgauge-issue-pickup/SKILL.md",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}
