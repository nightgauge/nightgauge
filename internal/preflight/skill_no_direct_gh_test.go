package preflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// makeSkill writes a SKILL.md file under the temp dir at skills/<name>/SKILL.md.
func makeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillNoDirectGH_CleanPassesEmpty(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "clean-one", "# clean skill\n\nnightgauge forge issue view 1 --repo o/r --json number\n")
	makeSkill(t, root, "clean-two", "no calls here\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected no findings, got %d: %+v", len(result.Findings), result.Findings)
	}
	if result.SkillsChecked != 2 {
		t.Errorf("skills_checked = %d, want 2", result.SkillsChecked)
	}
	if result.V != 1 {
		t.Errorf("schema version = %d, want 1", result.V)
	}
}

func TestSkillNoDirectGH_RegressionFlagsViolation(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "offender", "## Steps\n\n```bash\ngh issue view 42 --json number,title\n```\n")
	makeSkill(t, root, "innocent", "nightgauge forge issue list\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(result.Findings), result.Findings)
	}
	f := result.Findings[0]
	if f.SkillFile != filepath.Join("skills", "offender", "SKILL.md") {
		t.Errorf("skill_file = %q", f.SkillFile)
	}
	// Line 4: "## Steps", blank, "```bash", then the call. The fence is
	// part of the fixture now because the gate only reads fenced code —
	// prose that merely mentions the CLI is not a call. See
	// TestSkillNoDirectGH_ProseMentionIsNotAViolation.
	if f.Line != 4 {
		t.Errorf("line = %d, want 4", f.Line)
	}
	if f.Match == "" {
		t.Errorf("match should be non-empty: %+v", f)
	}
}

func TestSkillNoDirectGH_WordBoundary_DoesNotFlagGitHub(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "github-mention",
		"This skill targets the GitHub forge. Though gh-cli is not used, the word ghost is fine.\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// "gh-cli" matches `\bgh ` no — gh is followed by dash. "Though" doesn't
	// match because the `gh` is not at a word boundary. Word "ghost" — `gh`
	// at word boundary then 'o', so does not match `\bgh `.
	if len(result.Findings) != 0 {
		t.Errorf("unexpected findings for non-CLI gh mentions: %+v", result.Findings)
	}
}

func TestSkillNoDirectGH_IgnoresFilesOutsideSkillsGlob(t *testing.T) {
	root := t.TempDir()

	// Direct gh call in a non-skill file — should be ignored.
	notASkill := filepath.Join(root, "docs", "guide.md")
	if err := os.MkdirAll(filepath.Dir(notASkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notASkill, []byte("gh issue view 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Direct gh call in a nested test fixture under a skill — should be ignored
	// because the glob is skills/*/SKILL.md (exact file name SKILL.md only).
	nestedFixture := filepath.Join(root, "skills", "one", "tests", "fixture.md")
	if err := os.MkdirAll(filepath.Dir(nestedFixture), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedFixture, []byte("gh issue view 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeSkill(t, root, "one", "clean\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected no findings, got: %+v", result.Findings)
	}
}

func TestSkillNoDirectGH_MissingRoot_Errors(t *testing.T) {
	_, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: "/path/does/not/exist/anywhere"})
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestSkillNoDirectGH_AllowlistExemptsSkill(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "exempt-me", "## Steps\n```bash\ngh issue view 1\n```\n")
	makeSkill(t, root, "must-be-clean", "nightgauge forge issue view 1\n")

	allowlist := filepath.Join(root, "allowlist.txt")
	if err := os.WriteFile(allowlist, []byte("# comment\nexempt-me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{
		Root:          root,
		AllowlistPath: allowlist,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected 0 findings (skill is allowlisted), got: %+v", result.Findings)
	}
	if len(result.SkillsExempted) != 1 || result.SkillsExempted[0] != "exempt-me" {
		t.Errorf("skills_exempted = %v, want [exempt-me]", result.SkillsExempted)
	}
}

func TestSkillNoDirectGH_AllowlistMissing_NoExemption(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "offender", "```bash\ngh issue view 1\n```\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{
		Root:          root,
		AllowlistPath: filepath.Join(root, "does-not-exist.txt"),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Errorf("missing allowlist should not silently exempt: got %d findings", len(result.Findings))
	}
}

// TestSkillNoDirectGH_RealTreeIsClean runs the gate against this
// repository's actual skills/ directory, the way
// TestPlatformRawHTTP_RealPackageIsClean does for its own gate.
//
// Every other test in this file builds a synthetic skill in a temp dir. That
// is enough to prove the matcher works and nothing more, and for a long time
// it was all there was: the gate was in neither ci-local.sh nor lint.yml, so
// nothing ever pointed it at the real tree. It was meanwhile reporting two
// findings on main — both false positives, prose in a Markdown table — while
// 83 direct calls sat unexamined in `_includes/` and `_shared/`, which its
// `skills/*/SKILL.md` glob never opened. A gate whose only evidence is its
// own fixtures cannot tell you that.
//
// This test is the missing half. It fails on a new direct call in any file a
// stage executes, and it fails if the scan ever silently covers nothing.
func TestSkillNoDirectGH_RealTreeIsClean(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	res, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range res.Findings {
		t.Errorf("%s:%d direct GitHub-CLI call in an executed skill file: %s",
			f.SkillFile, f.Line, f.Match)
	}
	if res.SkillsChecked == 0 {
		t.Fatal("skills_checked = 0; the gate scanned nothing")
	}
	// The whole point of the widened scope. If this drops back to the
	// SKILL.md-only count, the glob regressed and the includes are dark
	// again.
	if res.SkillsChecked < 60 {
		t.Errorf("skills_checked = %d; expected the scan to cover SKILL.md plus _includes/ and _shared/ (the directories where the expensive calls live)", res.SkillsChecked)
	}
}

// TestSkillNoDirectGH_ProseMentionIsNotAViolation pins the scope decision
// that came with reading `_includes/` and `_shared/`: a skill is a Markdown
// document, and only the fenced code in it runs.
//
// Without this, the gate flags its own advice. `_shared/CI_GATE.md` spends
// two prose lines telling authors to NEVER hand-roll a `gh pr checks` poll,
// and the un-widened gate was reporting two findings on main that were both
// table cells describing a check, not performing one. Noise like that is how
// a gate gets allowlisted until it means nothing.
func TestSkillNoDirectGH_ProseMentionIsNotAViolation(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "prose", "Never hand-roll a `gh pr checks` poll.\n\n| B1 | `gh label list` per repo |\n")
	makeSkill(t, root, "comment-only", "```bash\n# fall back to gh run rerun when this fails\nnightgauge ci checks-complete\n```\n")

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range result.Findings {
		t.Errorf("prose flagged as a call: %s:%d %s", f.SkillFile, f.Line, f.Match)
	}
}

// TestSkillNoDirectGH_ScansIncludesAndShared is the regression for the scope
// hole itself: the old glob was skills/*/SKILL.md, so a clean SKILL.md that
// pulled a `_shared` include full of direct calls passed the gate. That is
// exactly how the whole-board pull in _shared/AUTO_SELECTION.md survived.
func TestSkillNoDirectGH_ScansIncludesAndShared(t *testing.T) {
	root := t.TempDir()
	makeSkill(t, root, "host", "nightgauge forge issue list\n")

	for _, f := range []struct{ dir, name string }{
		{filepath.Join("skills", "host", "_includes"), "step.md"},
		{filepath.Join("skills", "_shared"), "SHARED.md"},
	} {
		dir := filepath.Join(root, f.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		body := "```bash\ngh project item-list 3 --owner acme --format json\n```\n"
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}

	result, err := RunSkillNoDirectGHCheck(context.Background(), SkillNoDirectGHOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("expected 2 findings (one per included file), got %d: %+v", len(result.Findings), result.Findings)
	}
}
