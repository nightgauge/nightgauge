package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeIncludeFile writes rel (relative to root) with content, creating dirs.
func writeIncludeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func runIncludes(t *testing.T, root string) *SkillIncludesResult {
	t.Helper()
	res, err := RunSkillIncludesCheck(context.Background(), SkillIncludesOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return res
}

// ─── The #337 defect, both of its shapes ────────────────────────────────────

// TestSkillIncludes_FlagsTargetThatNeverExisted is the BATCH_MODE.md shape:
// the directive is well-formed, the file was simply never written.
func TestSkillIncludes_FlagsTargetThatNeverExisted(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/_shared/PIPELINE_CONTEXT.md", "shared\n")
	writeIncludeFile(t, root, "skills/nightgauge-feature-dev/SKILL.md", `---
name: feature-dev
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/BATCH_MODE.md -->

Body.
`)

	res := runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("expected exactly 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	f := res.Findings[0]
	if f.Target != "../_shared/BATCH_MODE.md" {
		t.Errorf("target = %q, want ../_shared/BATCH_MODE.md", f.Target)
	}
	if f.Line != 6 {
		t.Errorf("line = %d, want 6", f.Line)
	}
	if f.File != filepath.Join("skills", "nightgauge-feature-dev", "SKILL.md") {
		t.Errorf("file = %q", f.File)
	}
}

// TestSkillIncludes_FlagsMalformedDirectiveAndEchoesPathVerbatim is the
// EPIC_HANDLING shape: the FILE exists, but the trailing parenthetical is
// captured as part of the path, so the composer opens a name that does not.
// The finding must echo the captured path verbatim — that is what makes a
// malformed directive self-evident rather than sending a reader hunting for a
// file that is sitting right there.
func TestSkillIncludes_FlagsMalformedDirectiveAndEchoesPathVerbatim(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/_shared/EPIC_HANDLING.md", "shared\n")
	writeIncludeFile(t, root, "skills/nightgauge-assess-epic/SKILL.md",
		"<!-- include: ../_shared/EPIC_HANDLING.md (sub-issue fetch section) -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	want := "../_shared/EPIC_HANDLING.md (sub-issue fetch section)"
	if res.Findings[0].Target != want {
		t.Errorf("target = %q, want %q — the parenthetical is part of capture group 1", res.Findings[0].Target, want)
	}
	// The well-formed sibling directive for the same file must stay clean, so
	// the gate is flagging the malformation and not the file.
	writeIncludeFile(t, root, "skills/nightgauge-issue-pickup/SKILL.md",
		"<!-- include: ../_shared/EPIC_HANDLING.md -->\n")
	res = runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("well-formed directive to the same file must not be flagged, got %+v", res.Findings)
	}
}

// ─── Scope ──────────────────────────────────────────────────────────────────

func TestSkillIncludes_ResolvingDirectivesAreClean(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/_shared/GOTCHAS.md", "shared\n")
	writeIncludeFile(t, root, "skills/nightgauge-x/_includes/detail.md", "detail\n")
	writeIncludeFile(t, root, "skills/nightgauge-x/SKILL.md",
		"<!-- include: ../_shared/GOTCHAS.md -->\n<!-- include: _includes/detail.md -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 0 {
		t.Fatalf("expected clean tree, got %+v", res.Findings)
	}
	if res.FilesChecked != 3 {
		t.Errorf("files checked = %d, want 3", res.FilesChecked)
	}
}

// TestSkillIncludes_ReadmeExamplesAreNotScanned pins the scope decision.
// skills/README.md documents the directive shape in prose and in fenced
// examples; a gate that scanned it would fail CI on the documentation.
func TestSkillIncludes_ReadmeExamplesAreNotScanned(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/nightgauge-x/SKILL.md", "clean\n")
	writeIncludeFile(t, root, "skills/README.md", "These are pulled in via `<!-- include: ../_shared/NOPE.md -->`.\n")
	writeIncludeFile(t, root, "skills/nightgauge-x/NOTES.md", "<!-- include: ../_shared/ALSO_NOPE.md -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 0 {
		t.Fatalf("only SKILL.md/_includes/_shared are in scope, got %+v", res.Findings)
	}
}

// TestSkillIncludes_ScansSupportingFiles — a dead include inside an _includes/
// or _shared/ file reaches the model the same way as one in a SKILL.md.
func TestSkillIncludes_ScansSupportingFiles(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/nightgauge-x/_includes/detail.md", "<!-- include: ../../_shared/MISSING.md -->\n")
	writeIncludeFile(t, root, "skills/_shared/SHARED.md", "<!-- include: OTHER_MISSING.md -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(res.Findings), res.Findings)
	}
}

// TestSkillIncludes_SupportingFileResolvesAgainstSkillDir pins the gate to the
// composer's resolution rule. skillrender.ExpandIncludes always joins the
// captured path to the SKILL.md's directory (render.go), never to the directory
// of the file carrying the directive — for a directive inside _includes/ those
// differ by one level. A gate that used the carrying file's directory would
// report a path the composer never tries, which is the gate/composer
// disagreement this whole check exists to avoid.
func TestSkillIncludes_SupportingFileResolvesAgainstSkillDir(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/nightgauge-x/SKILL.md", "clean\n")
	writeIncludeFile(t, root, "skills/nightgauge-x/_includes/detail.md", "<!-- include: ../_shared/MISSING.md -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", res.Findings)
	}
	// skillDir is skills/nightgauge-x, so ../_shared/MISSING.md is
	// skills/_shared/MISSING.md — NOT skills/nightgauge-x/_shared/MISSING.md.
	want := filepath.Join("skills", "_shared", "MISSING.md")
	if res.Findings[0].Resolved != want {
		t.Errorf("resolved = %q, want %q (the composer's skillDir join)", res.Findings[0].Resolved, want)
	}
}

// TestSkillIncludes_ScansPluginMirror — the generated Claude-plugin tree is
// what plugin sessions are served from, so a fix applied only to skills/ still
// ships the dead include until the mirror is regenerated.
func TestSkillIncludes_ScansPluginMirror(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/nightgauge-x/SKILL.md", "clean\n")
	writeIncludeFile(t, root, "claude-plugins/nightgauge/skills/x/SKILL.md",
		"<!-- include: ../_shared/BATCH_MODE.md -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("expected the mirror to be scanned, got %+v", res.Findings)
	}
	if !strings.HasPrefix(res.Findings[0].File, "claude-plugins") {
		t.Errorf("finding should come from the plugin mirror, got %q", res.Findings[0].File)
	}
}

func TestSkillIncludes_DirectoryTargetIsNotAFile(t *testing.T) {
	root := t.TempDir()
	writeIncludeFile(t, root, "skills/_shared/dir/keep.md", "x\n")
	writeIncludeFile(t, root, "skills/nightgauge-x/SKILL.md", "<!-- include: ../_shared/dir -->\n")

	res := runIncludes(t, root)
	if len(res.Findings) != 1 {
		t.Fatalf("a directory is not a readable include target, got %+v", res.Findings)
	}
}

func TestSkillIncludes_MissingTreesAreCleanNoOp(t *testing.T) {
	res := runIncludes(t, t.TempDir())
	if res.FilesChecked != 0 || len(res.Findings) != 0 {
		t.Fatalf("absent trees should be a clean no-op, got %+v", res)
	}
}

func TestSkillIncludes_MissingRootErrors(t *testing.T) {
	_, err := RunSkillIncludesCheck(context.Background(), SkillIncludesOptions{
		Root: filepath.Join(t.TempDir(), "nope"),
	})
	if err == nil {
		t.Fatal("expected an error for an unresolvable root")
	}
}

// ─── The gate ───────────────────────────────────────────────────────────────

// TestSkillIncludes_WorkingTreeIsClean is the enforcement. Nothing in CI runs
// `nightgauge preflight` subcommands or the lint-skills shell scripts, but CI
// does run `go test ./...` — so this assertion is what actually stops a dead
// include from shipping to a model (#337).
//
// Against the pre-fix tree it fails with 14 findings, reproduced against a
// pristine `main` checkout (`files checked: 230  dead includes: 14`): seven
// under skills/ — six well-formed `../_shared/BATCH_MODE.md` directives against
// a file that was never written, plus the malformed
// `../_shared/EPIC_HANDLING.md (sub-issue fetch section)` in assess-epic — and
// all seven again in the generated plugin tree, the malformed one included.
func TestSkillIncludes_WorkingTreeIsClean(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "skills")); err != nil {
		t.Skipf("skills/ not present at %s: %v", root, err)
	}
	res := runIncludes(t, root)
	if len(res.Findings) != 0 {
		var b strings.Builder
		for _, f := range res.Findings {
			b.WriteString("\n  " + f.File + ":" + strconv.Itoa(f.Line) + "  " + f.Directive +
				"\n      captured path: " + f.Target + "\n      resolves to:   " + f.Resolved)
		}
		t.Fatalf("%d include directive(s) do not resolve — the literal comment ships to the model in place of the shared content:%s",
			len(res.Findings), b.String())
	}
	if res.FilesChecked == 0 {
		t.Fatal("expected to scan skill files, scanned 0")
	}
}
