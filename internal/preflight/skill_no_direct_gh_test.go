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
	makeSkill(t, root, "offender", "## Steps\n\ngh issue view 42 --json number,title\n")
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
	if f.Line != 3 {
		t.Errorf("line = %d, want 3", f.Line)
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
	makeSkill(t, root, "exempt-me", "## Steps\ngh issue view 1\n")
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
	makeSkill(t, root, "offender", "gh issue view 1\n")

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
