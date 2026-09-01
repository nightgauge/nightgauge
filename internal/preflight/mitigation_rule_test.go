package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mitigationRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Both required skills state the rule, so the fixture starts clean and each
	// negative case removes exactly one thing.
	for _, skill := range RequiredRuleSkills {
		dir := filepath.Join(root, "skills", skill)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		body := "# " + skill + "\n\n- **" + RuleMarker + "** — do not land a retry for an unobserved mechanism.\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func writeSource(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func runMitigation(t *testing.T, root string) *MitigationRuleResult {
	t.Helper()
	res, err := RunMitigationRuleCheck(context.Background(), MitigationRuleOptions{Root: root})
	if err != nil {
		t.Fatalf("RunMitigationRuleCheck: %v", err)
	}
	return res
}

func checks(res *MitigationRuleResult) string {
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.Check+"@"+f.File)
	}
	return strings.Join(out, ",")
}

func TestMitigationRule_CleanTreePasses(t *testing.T) {
	res := runMitigation(t, mitigationRoot(t))
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings, got %s", checks(res))
	}
	if res.SkillsChecked != len(RequiredRuleSkills) {
		t.Errorf("skills checked = %d, want %d", res.SkillsChecked, len(RequiredRuleSkills))
	}
}

// TestMitigationRule_MissingRuleIsCaught is the "Presence" half: the guidance
// quietly disappearing from a skill during an edit is a silent failure, and
// silence is how the two unverified mitigations survived review the first time.
func TestMitigationRule_MissingRuleIsCaught(t *testing.T) {
	root := mitigationRoot(t)
	path := filepath.Join(root, "skills", RequiredRuleSkills[0], "SKILL.md")
	if err := os.WriteFile(path, []byte("# nothing here\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res := runMitigation(t, root)
	if !strings.Contains(checks(res), CheckRuleMissing) {
		t.Fatalf("findings = %s, want a rule_missing finding", checks(res))
	}
}

// TestMitigationRule_UntrackedMarkerIsCaught is the "Markers" half.
func TestMitigationRule_UntrackedMarkerIsCaught(t *testing.T) {
	root := mitigationRoot(t)
	// Built from the constant rather than written as a literal: a test fixture
	// that spells the marker out would itself be scanned, and a gate whose own
	// tests trip it is a gate people learn to skip.
	writeSource(t, root, "internal/app/retry.go",
		"package app\n\n// "+MitigationMarkerPrefix+" mechanism=unobserved\nfunc retry() {}\n")

	res := runMitigation(t, root)
	if !strings.Contains(checks(res), CheckMarkerUntracked) {
		t.Fatalf("findings = %s, want a marker_untracked finding", checks(res))
	}
	if res.Findings[0].Line != 3 {
		t.Errorf("line = %d, want 3", res.Findings[0].Line)
	}
}

// TestMitigationRule_TrackedMarkerPasses — deliberate mitigation is allowed.
// The gate refuses the undertaking nobody recorded, not the decision.
func TestMitigationRule_TrackedMarkerPasses(t *testing.T) {
	root := mitigationRoot(t)
	writeSource(t, root, "internal/app/retry.go",
		"package app\n\n// "+MitigationMarkerPrefix+" issue=owner/repo#123 mechanism=unobserved\nfunc retry() {}\n")

	if res := runMitigation(t, root); len(res.Findings) != 0 {
		t.Fatalf("a tracked marker must pass, got %s", checks(res))
	}
}

// TestMitigationRule_MarkerInAStringLiteralIsNotAMarker — the token appears in
// the gate's own source and in fixtures that build markers as data. A gate that
// cannot tell its own definition from a violation fails on its first run.
func TestMitigationRule_MarkerInAStringLiteralIsNotAMarker(t *testing.T) {
	root := mitigationRoot(t)
	writeSource(t, root, "internal/app/const.go",
		"package app\n\nconst prefix = \""+MitigationMarkerPrefix+"\"\n")

	if res := runMitigation(t, root); len(res.Findings) != 0 {
		t.Fatalf("a string literal is not a marker, got %s", checks(res))
	}
}

// TestMitigationRule_EmptyIssueValueIsUntracked — `issue=` followed by nothing
// is the same undertaking-nobody-recorded as no marker field at all.
func TestMitigationRule_EmptyIssueValueIsUntracked(t *testing.T) {
	root := mitigationRoot(t)
	writeSource(t, root, "internal/app/retry.go",
		"package app\n\n// "+MitigationMarkerPrefix+" issue= mechanism=unobserved\n")

	if res := runMitigation(t, root); !strings.Contains(checks(res), CheckMarkerUntracked) {
		t.Fatalf("findings = %s, want a marker_untracked finding", checks(res))
	}
}

// TestMitigationRule_RuleMayLiveInASupportingFile — demanding a particular file
// would be a rule about filing rather than about the guidance.
func TestMitigationRule_RuleMayLiveInASupportingFile(t *testing.T) {
	root := mitigationRoot(t)
	skill := RequiredRuleSkills[0]
	if err := os.WriteFile(filepath.Join(root, "skills", skill, "SKILL.md"), []byte("# skill\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeSource(t, root, "skills/"+skill+"/_includes/rules.md", "**"+RuleMarker+"** applies here.\n")

	if res := runMitigation(t, root); len(res.Findings) != 0 {
		t.Fatalf("expected the supporting file to satisfy presence, got %s", checks(res))
	}
}

func TestMitigationRule_SkipsVendoredTrees(t *testing.T) {
	root := mitigationRoot(t)
	writeSource(t, root, "node_modules/pkg/index.js",
		"// "+MitigationMarkerPrefix+" mechanism=unobserved\n")

	if res := runMitigation(t, root); len(res.Findings) != 0 {
		t.Fatalf("node_modules is not ours to police, got %s", checks(res))
	}
}

func TestMitigationRule_UnreadableRootErrors(t *testing.T) {
	if _, err := RunMitigationRuleCheck(context.Background(),
		MitigationRuleOptions{Root: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("expected an error for a root that does not exist")
	}
}

// TestMitigationRule_WorkingTreeIsClean is the enforcement path, not a
// self-test of the fixtures above. `go test ./internal/preflight/` runs
// ungated in CI precisely so tree-content guards cannot be skipped by a
// change-class gate, and a docs-only edit that drops the rule from a SKILL.md
// is exactly the shape that would slip past one.
func TestMitigationRule_WorkingTreeIsClean(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "skills")); err != nil {
		t.Skipf("skills/ not present at %s: %v", root, err)
	}
	res := runMitigation(t, root)
	if len(res.Findings) != 0 {
		var b strings.Builder
		for _, f := range res.Findings {
			b.WriteString("\n  [" + f.Check + "] " + f.File + "  " + f.Match + "\n      " + f.Message)
		}
		t.Fatalf("%d mitigation-rule finding(s):%s", len(res.Findings), b.String())
	}
	if res.FilesScanned == 0 {
		t.Fatal("expected to scan source files, scanned 0")
	}
}
