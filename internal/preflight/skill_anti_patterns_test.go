package preflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeSkillFile writes content to skills/<rel> under root, creating dirs.
func writeSkillFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, "skills", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countByCheck returns how many findings carry the given check id.
func countByCheck(findings []SkillAntiPattern, check string) int {
	n := 0
	for _, f := range findings {
		if f.Check == check {
			n++
		}
	}
	return n
}

func run(t *testing.T, root string) *SkillAntiPatternsResult {
	t.Helper()
	res, err := RunSkillAntiPatternsCheck(context.Background(), SkillAntiPatternsOptions{Root: root})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

// --- Check A: nested references ---

func TestAntiPatterns_NestedRef_FlagsSupportingFilePointingAtAnother(t *testing.T) {
	root := t.TempDir()
	// A two-level chain: SKILL.md → _includes/a.md → _includes/b.md.
	// The middle file is the offender (a supporting file referencing another).
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\n> **Read `skills/demo/_includes/a.md` now** before continuing.\n")
	writeSkillFile(t, root, "demo/_includes/a.md",
		"# A\n\nDo the thing.\n\n> **Read `skills/demo/_includes/b.md` now** for more.\n")
	writeSkillFile(t, root, "demo/_includes/b.md", "# B\n\nLeaf.\n")

	res := run(t, root)
	if got := countByCheck(res.Findings, CheckNestedReference); got != 1 {
		t.Fatalf("nested_reference findings = %d, want 1: %+v", got, res.Findings)
	}
	f := findFirst(res.Findings, CheckNestedReference)
	if f.File != filepath.Join("skills", "demo", "_includes", "a.md") {
		t.Errorf("file = %q", f.File)
	}
	if f.Line != 5 {
		t.Errorf("line = %d, want 5", f.Line)
	}
}

func TestAntiPatterns_NestedRef_IncludeDirectiveInSupportingFile(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/_shared/PARENT.md",
		"### Parent\n\n<!-- include: ../_shared/CHILD.md -->\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckNestedReference); got != 1 {
		t.Fatalf("nested_reference findings = %d, want 1: %+v", got, res.Findings)
	}
}

func TestAntiPatterns_NestedRef_OneLevelChainIsClean(t *testing.T) {
	root := t.TempDir()
	// SKILL.md may reference a supporting file (one level — allowed).
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\n> **Read `skills/demo/_includes/a.md` now** before continuing.\n<!-- include: ../_shared/PREFLIGHT.md -->\n")
	writeSkillFile(t, root, "demo/_includes/a.md", "# A\n\nLeaf with no further reference.\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckNestedReference); got != 0 {
		t.Fatalf("nested_reference findings = %d, want 0 (SKILL.md refs are level 1): %+v", got, res.Findings)
	}
}

func TestAntiPatterns_NestedRef_ProseReadIsNotFlagged(t *testing.T) {
	root := t.TempDir()
	// Runtime data reads — NOT structural supporting-file references.
	writeSkillFile(t, root, "demo/_includes/a.md",
		"# A\n\nRead PLAN.md and extract the acceptance criteria.\n"+
			"Read CLAUDE.md and AGENTS.md for conventions.\n"+
			"See decisions.md and PRD.md from the knowledge path.\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckNestedReference); got != 0 {
		t.Fatalf("nested_reference findings = %d, want 0 (prose data reads): %+v", got, res.Findings)
	}
}

// --- Check B: backslash paths ---

func TestAntiPatterns_Backslash_FlagsWindowsPath(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\nEdit skills\\demo\\SKILL.md to add the step.\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckBackslashPath); got != 1 {
		t.Fatalf("backslash_path findings = %d, want 1: %+v", got, res.Findings)
	}
	f := findFirst(res.Findings, CheckBackslashPath)
	if f.Line != 3 {
		t.Errorf("line = %d, want 3", f.Line)
	}
}

func TestAntiPatterns_Backslash_FlagsFileToken(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/SKILL.md", "# demo\n\nOpen config\\settings.json now.\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckBackslashPath); got != 1 {
		t.Fatalf("backslash_path findings = %d, want 1: %+v", got, res.Findings)
	}
}

func TestAntiPatterns_Backslash_RegexEscapesAreClean(t *testing.T) {
	root := t.TempDir()
	// Regex / escape contexts must NOT be flagged.
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\nThe pattern \\bgh matches; \\d+ digits, \\w words, \\. dot, \\n newline.\n"+
			"A path like docs/guide.md uses forward slashes (correct).\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckBackslashPath); got != 0 {
		t.Fatalf("backslash_path findings = %d, want 0 (regex escapes): %+v", got, res.Findings)
	}
}

// --- Check C: missing TOC ---

func TestAntiPatterns_TOC_FlagsLongFileWithoutContents(t *testing.T) {
	root := t.TempDir()
	body := "### Long Partial\n\n"
	for i := 0; i < tocMinLines+10; i++ {
		body += "line of content\n"
	}
	writeSkillFile(t, root, "demo/_shared/LONG.md", body)
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckMissingTOC); got != 1 {
		t.Fatalf("missing_toc findings = %d, want 1: %+v", got, res.Findings)
	}
	f := findFirst(res.Findings, CheckMissingTOC)
	if f.Line != 0 {
		t.Errorf("line = %d, want 0 (whole-file finding)", f.Line)
	}
}

func TestAntiPatterns_TOC_LongFileWithContentsIsClean(t *testing.T) {
	root := t.TempDir()
	body := "### Long Partial\n\n## Contents\n\n- [Section](#section)\n\n"
	for i := 0; i < tocMinLines+10; i++ {
		body += "line of content\n"
	}
	writeSkillFile(t, root, "demo/_includes/long.md", body)
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckMissingTOC); got != 0 {
		t.Fatalf("missing_toc findings = %d, want 0 (has Contents): %+v", got, res.Findings)
	}
}

func TestAntiPatterns_TOC_ShortFileIsClean(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/_shared/SHORT.md", "### Short\n\nA few lines.\nNo TOC needed.\n")
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckMissingTOC); got != 0 {
		t.Fatalf("missing_toc findings = %d, want 0 (short file): %+v", got, res.Findings)
	}
}

func TestAntiPatterns_TOC_OnlyAppliesToSupportingFiles(t *testing.T) {
	root := t.TempDir()
	// A long SKILL.md body without a TOC must NOT be flagged (Check C is
	// scoped to supporting files — SKILL.md bodies are owned by the
	// progressive-disclosure refactor sub-issues).
	body := "# Long Skill\n\n"
	for i := 0; i < tocMinLines+10; i++ {
		body += "line of content\n"
	}
	writeSkillFile(t, root, "demo/SKILL.md", body)
	res := run(t, root)
	if got := countByCheck(res.Findings, CheckMissingTOC); got != 0 {
		t.Fatalf("missing_toc findings = %d, want 0 (SKILL.md is out of scope): %+v", got, res.Findings)
	}
}

// --- Walker hygiene ---

func TestAntiPatterns_SkipsBakFiles(t *testing.T) {
	root := t.TempDir()
	// A .bak editor backup must be skipped entirely (extension != .md), even
	// if it contains a backslash path.
	bak := filepath.Join(root, "skills", "demo", "SKILL.md.bak")
	if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bak, []byte("Edit skills\\demo\\SKILL.md here.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, root, "demo/SKILL.md", "# clean\n\nNo issues here.\n")
	res := run(t, root)
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings (.bak skipped), got: %+v", res.Findings)
	}
}

func TestAntiPatterns_CleanTreeIsEmpty(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\n> **Read `skills/demo/_includes/a.md` now**.\n")
	writeSkillFile(t, root, "demo/_includes/a.md", "# A\n\nClean leaf, forward/slashes only.\n")
	res := run(t, root)
	if len(res.Findings) != 0 {
		t.Errorf("expected no findings, got: %+v", res.Findings)
	}
	if res.V != 1 {
		t.Errorf("schema version = %d, want 1", res.V)
	}
	if res.FilesChecked != 2 {
		t.Errorf("files_checked = %d, want 2", res.FilesChecked)
	}
}

// --- Check D: admin merge bypass ---

func TestAntiPatterns_AdminMerge_FlagsInvocationAndAdvertisement(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\n/nightgauge-pr-merge --admin\n")
	writeSkillFile(t, root, "demo/_includes/m.md",
		"# M\n\n```bash\ngh pr merge \"$PR\" --squash --admin\n```\n")
	writeSkillFile(t, root, "demo/_includes/auto.md",
		"# Auto\n\n```bash\ngh pr merge 42 --auto\n```\n")

	res := run(t, root)
	if got := countByCheck(res.Findings, CheckAdminMergeBypass); got != 3 {
		t.Fatalf("admin_merge_bypass findings = %d, want 3: %+v", got, res.Findings)
	}
}

func TestAntiPatterns_AdminMerge_AutoFixFlagIsClean(t *testing.T) {
	root := t.TempDir()
	// --auto-fix is a legitimate skill argument — must not match --auto.
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\n/nightgauge-pr-merge --auto-fix\n")

	res := run(t, root)
	if got := countByCheck(res.Findings, CheckAdminMergeBypass); got != 0 {
		t.Fatalf("--auto-fix falsely flagged: %+v", res.Findings)
	}
}

func TestAntiPatterns_AdminMerge_ProhibitionProseIsClean(t *testing.T) {
	root := t.TempDir()
	// Prohibition text mentions the flag without a same-line merge invocation
	// — must NOT be flagged (the guard exists to catch advertisements, not
	// the rules that ban them).
	writeSkillFile(t, root, "demo/SKILL.md",
		"# demo\n\nNever pass `--admin` or `--auto`; merges are manual squash only.\n"+
			"A blocked merge is terminal — escalate instead.\n")

	res := run(t, root)
	if got := countByCheck(res.Findings, CheckAdminMergeBypass); got != 0 {
		t.Fatalf("prohibition prose flagged: %+v", res.Findings)
	}
}

func TestAntiPatterns_MissingRoot_Errors(t *testing.T) {
	_, err := RunSkillAntiPatternsCheck(context.Background(), SkillAntiPatternsOptions{Root: "/path/does/not/exist/anywhere"})
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

// findFirst returns the first finding with the given check, or a zero value.
func findFirst(findings []SkillAntiPattern, check string) SkillAntiPattern {
	for _, f := range findings {
		if f.Check == check {
			return f
		}
	}
	return SkillAntiPattern{}
}
