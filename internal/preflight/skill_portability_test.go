package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Uses the shared writeSkillFile(t, root, rel, content) helper from
// skill_anti_patterns_test.go — `rel` is the path under <root>/skills/.

func TestSkillPortability_FlagsVSCodeExtensionPath(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/SKILL.md", `---
name: pr-create
---
Resolve the binary:
`+"```bash"+`
for cand in "$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge; do
  [ -x "$cand" ] && BINARY="$cand" && break
done
`+"```"+`
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Check != CheckVSCodeBinaryPath {
		t.Errorf("check = %q, want %q", f.Check, CheckVSCodeBinaryPath)
	}
	if f.SkillFile != filepath.Join("skills", "nightgauge-pr-create", "SKILL.md") {
		t.Errorf("skill_file = %q", f.SkillFile)
	}
	if f.Line != 6 {
		t.Errorf("line = %d, want 6", f.Line)
	}
}

func TestSkillPortability_FlagsIncludesAndShared(t *testing.T) {
	root := t.TempDir()
	// A finding in an _includes file…
	writeSkillFile(t, root, "nightgauge-pr-merge/_includes/merge.md",
		`cand="$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`)
	// …and in a _shared file.
	writeSkillFile(t, root, "_shared/PREFLIGHT.md",
		`glob "$HOME"/.vscode/extensions/nightgauge.foo/bin`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 findings (includes + shared), got %d: %+v", len(res.Findings), res.Findings)
	}
}

func TestSkillPortability_CleanCascadePasses(t *testing.T) {
	root := t.TempDir()
	// The post-#4029 provider-neutral cascade — no VSCode path.
	writeSkillFile(t, root, "nightgauge-feature-dev/SKILL.md", `---
name: feature-dev
---
`+"```bash"+`
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
`+"```"+`
`)
	if res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else if len(res.Findings) != 0 {
		t.Fatalf("clean cascade should produce no findings, got %+v", res.Findings)
	}
}

func TestSkillPortability_IgnoresWorkspaceManifestDotVscode(t *testing.T) {
	root := t.TempDir()
	// workspace-init skills write a `<root>/.vscode/` manifest dir — that is the
	// project workspace folder, NOT the extension glob, and must not be flagged.
	writeSkillFile(t, root, "nightgauge-workspace-init/_includes/manifest-generation.md",
		`mkdir -p "$WORKSPACE_ROOT/.vscode"`)
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("workspace .vscode manifest dir must not be flagged, got %+v", res.Findings)
	}
}

func TestSkillPortability_SkipsClaudeRuntimeDir(t *testing.T) {
	root := t.TempDir()
	// Ephemeral runtime-memory files under .claude/ are not skill source and are
	// excluded (keeps the Go scan in step with the shell mirror's rg). A vscode
	// path here must NOT be flagged.
	writeSkillFile(t, root, "nightgauge-product-audit/.claude/agent-memory/x.md",
		`note: "$HOME"/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`)
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf(".claude runtime dir must be excluded, got %+v", res.Findings)
	}
}

func TestSkillPortability_MatchIsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-merge/SKILL.md",
		`cand="$HOME"/.vscode/extensions/Nightgauge.nightgauge-vscode/dist/bin/nightgauge`)
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("case-variant vscode path should be flagged, got %+v", res.Findings)
	}
}

func TestSkillPortability_NoSkillsDirIsClean(t *testing.T) {
	root := t.TempDir()
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesChecked != 0 || len(res.Findings) != 0 {
		t.Fatalf("missing skills/ dir should be a clean no-op, got %+v", res)
	}
}

// TestSkillPortability_WorkingTreeIsClean is the positive gate: the real
// skills/ tree must contain zero VSCode-extension paths after #4029.
func TestSkillPortability_WorkingTreeIsClean(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "skills")); statErr != nil {
		t.Skipf("skills/ not found at %s; skipping working-tree assertion", repoRoot)
	}
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: repoRoot})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("working tree must be portable (no .vscode/extensions paths), got %d findings: %+v",
			len(res.Findings), res.Findings)
	}
	if res.FilesChecked == 0 {
		t.Fatalf("expected to scan skill files, scanned 0")
	}
}

// ─── #55: Stop-hook ban + truncated-cascade drift guard ─────────────────────

func TestSkillPortability_FlagsHooksFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/SKILL.md", `---
name: feature-dev
hooks:
  Stop:
    - hooks:
        - type: agent
          prompt: verify completion
---
Body.
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	if res.Findings[0].Check != CheckStopHook {
		t.Errorf("check = %q, want %q", res.Findings[0].Check, CheckStopHook)
	}
	if res.Findings[0].Line != 3 {
		t.Errorf("line = %d, want 3", res.Findings[0].Line)
	}
}

func TestSkillPortability_HooksInIncludesNotFlagged(t *testing.T) {
	// Only SKILL.md frontmatter carries hooks; a docs include DISCUSSING the
	// directive (e.g. SKILL_PORTABILITY excerpts) must not trip the gate.
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/_includes/notes.md", `The old design used a
hooks:
key in frontmatter — removed in #55.
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings, got %+v", res.Findings)
	}
}

func TestSkillPortability_FlagsTruncatedCascade(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/_includes/gate.md", "```bash"+`
BINARY="${NIGHTGAUGE_BIN:-}"
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
`+"```"+`
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	if res.Findings[0].Check != CheckTruncatedBinaryCascade {
		t.Errorf("check = %q, want %q", res.Findings[0].Check, CheckTruncatedBinaryCascade)
	}
}

func TestSkillPortability_FullCascadePasses(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-feature-dev/_includes/gate.md", "```bash"+`
BINARY="${NIGHTGAUGE_BIN:-}"
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
`+"```"+`
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings, got %+v", res.Findings)
	}
}

// TestSkillPortability_FullCascadeCannotHideTruncatedSibling is derived from
// post-merge.md's #365 defect: several complete resolver blocks made a later
// truncated block pass the old file-level `go/bin/nightgauge` check.
func TestSkillPortability_FullCascadeCannotHideTruncatedSibling(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-merge/_includes/post-merge.md", `# Post merge

`+"```bash"+`
BINARY="${NIGHTGAUGE_BIN:-}"
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
[ -z "$BINARY" ] && [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
`+"```"+`

Later cleanup block:

`+"```bash"+`
BINARY="${NIGHTGAUGE_BIN:-}"
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
`+"```"+`
`)

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected the truncated second block to produce 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Check != CheckTruncatedBinaryCascade {
		t.Errorf("check = %q, want %q", f.Check, CheckTruncatedBinaryCascade)
	}
	if f.Line != 15 {
		t.Errorf("line = %d, want truncated cascade start at line 15", f.Line)
	}
	for _, want := range []string{"bash block line 14", "repo bin", "canonical repo bin"} {
		if !strings.Contains(f.Match, want) {
			t.Errorf("finding must name offending block/missing rung %q, got %q", want, f.Match)
		}
	}
}
