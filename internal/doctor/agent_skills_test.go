package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillVersion(t *testing.T, dir, version string, marked bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: x\nmetadata:\n  author: nightgauge\n  version: \"" + version + "\"\n---\n\n# s\n"
	if version == "" {
		body = "---\nname: x\n---\n\n# s\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if marked {
		if err := os.WriteFile(filepath.Join(dir, agentSkillsMarker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// agentSkillsFixture is a build with nightgauge-retro at 1.0.0 and an empty
// Codex skills directory.
func agentSkillsFixture(t *testing.T) (home agentSkillsHome, roots []string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "skills")
	writeSkillVersion(t, filepath.Join(root, agentSkillsEntry), "1.0.0", false)
	writeSkillVersion(t, filepath.Join(root, "nightgauge-retro"), "1.0.0", false)
	skills := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	return agentSkillsHome{"Codex", skills}, []string{root}
}

func TestAgentSkillsMatchingVersionIsClean(t *testing.T) {
	home, roots := agentSkillsFixture(t)
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-retro"), "1.0.0", true)
	fs, detail := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
	if len(fs) != 0 {
		t.Fatalf("findings = %s; want none", findingsText(fs))
	}
	if detail != "Codex: 1 checked, 0 differ" {
		t.Errorf("detail = %q; the skill was not really compared", detail)
	}
}

func TestAgentSkillsReportsDriftWithBothVersions(t *testing.T) {
	home, roots := agentSkillsFixture(t)
	dir := filepath.Join(home.skills, "nightgauge-retro")
	writeSkillVersion(t, dir, "0.9.0", true)
	fs, _ := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
	if len(fs) != 1 {
		t.Fatalf("findings = %s; want one", findingsText(fs))
	}
	f := fs[0]
	if f.Code != agentSkillsCode || f.Severity != SeverityWarning {
		t.Errorf("code %s severity %v", f.Code, f.Severity)
	}
	if f.Evidence["installed"] != "0.9.0" || f.Evidence["build"] != "1.0.0" || f.Evidence["path"] != dir {
		t.Errorf("evidence = %v", f.Evidence)
	}
}

func TestAgentSkillsReportsMissingInstalledVersion(t *testing.T) {
	home, roots := agentSkillsFixture(t)
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-retro"), "", true)
	fs, _ := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
	if len(fs) != 1 || !strings.Contains(fs[0].Cause, "(none)") {
		t.Fatalf("findings = %s; want a missing-version finding", findingsText(fs))
	}
}

func TestAgentSkillsMarkerlessPointsAtAdopt(t *testing.T) {
	home, roots := agentSkillsFixture(t)
	// A matching version still needs adopting: every later install skips it.
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-retro"), "1.0.0", false)
	fs, _ := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
	if len(fs) != 1 || !strings.Contains(findingsText(fs), "no install marker") {
		t.Fatalf("findings = %s; want a marker finding", findingsText(fs))
	}
	if !strings.Contains(strings.Join(fs[0].Remedies[0].Steps, " "), "--adopt") {
		t.Errorf("remedy does not name --adopt: %v", fs[0].Remedies[0].Steps)
	}
}

func TestAgentSkillsRetired(t *testing.T) {
	for _, marked := range []bool{true, false} {
		home, roots := agentSkillsFixture(t)
		writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-backlog-audit"), "1.0.0", marked)
		fs, _ := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
		if len(fs) != 1 || !strings.Contains(fs[0].Title, "retired") {
			t.Fatalf("marked=%v findings = %s; want retired", marked, findingsText(fs))
		}
	}
}

func TestAgentSkillsLeavesOperatorEntriesAlone(t *testing.T) {
	home, roots := agentSkillsFixture(t)
	writeSkillVersion(t, filepath.Join(home.skills, "update-docs"), "9.9.9", false)
	writeSkillVersion(t, filepath.Join(home.skills, "_shared"), "9.9.9", true)
	target := t.TempDir()
	writeSkillVersion(t, target, "9.9.9", true)
	if err := os.Symlink(target, filepath.Join(home.skills, "nightgauge-linked")); err != nil {
		t.Fatal(err)
	}
	fs, detail := agentSkillsFindingsIn([]agentSkillsHome{home}, roots)
	if len(fs) != 0 {
		t.Fatalf("findings = %s; want none", findingsText(fs))
	}
	if detail != "Codex: 0 checked, 0 differ" {
		t.Errorf("detail = %q", detail)
	}
}

func TestAgentSkillsNoHomeIsNotAFinding(t *testing.T) {
	_, roots := agentSkillsFixture(t)
	missing := agentSkillsHome{"Grok", filepath.Join(t.TempDir(), "absent", "skills")}
	fs, detail := agentSkillsFindingsIn([]agentSkillsHome{missing}, roots)
	if len(fs) != 0 || !strings.Contains(detail, "no skills directory") {
		t.Fatalf("findings = %s, detail %q", findingsText(fs), detail)
	}
}

func TestAgentSkillsNoSkillsTreeIsSkipped(t *testing.T) {
	home, _ := agentSkillsFixture(t)
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-retro"), "1.0.0", true)
	fs, detail := agentSkillsFindingsIn([]agentSkillsHome{home}, []string{filepath.Join(t.TempDir(), "gone")})
	if len(fs) != 0 || !strings.HasPrefix(detail, "skipped") {
		t.Fatalf("findings = %s, detail %q", findingsText(fs), detail)
	}
}

// Run in another project by a binary with no bundle, the only root is that
// project's own skills/. Nothing there says a Nightgauge skill was retired.
func TestAgentSkillsIgnoresAnotherProjectsSkillsTree(t *testing.T) {
	home, _ := agentSkillsFixture(t)
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-retro"), "1.0.0", true)
	foreign := filepath.Join(t.TempDir(), "skills")
	writeSkillVersion(t, filepath.Join(foreign, "mine"), "1.0.0", false)
	fs, detail := agentSkillsFindingsIn([]agentSkillsHome{home}, []string{foreign})
	if len(fs) != 0 || !strings.HasPrefix(detail, "skipped") {
		t.Fatalf("findings = %s, detail %q", findingsText(fs), detail)
	}
}

// The extension's dist/skills is a subset; the rest of the build is the mirror
// beside it, stored without the prefix.
func TestAgentSkillsResolvesMirrorReference(t *testing.T) {
	home, _ := agentSkillsFixture(t)
	dist := t.TempDir()
	root := filepath.Join(dist, "skills")
	writeSkillVersion(t, filepath.Join(root, agentSkillsEntry), "1.0.0", false)
	writeSkillVersion(t, filepath.Join(dist, "claude-plugins", "nightgauge", "skills", "issue-audit"), "2.0.0", false)
	writeSkillVersion(t, filepath.Join(home.skills, "nightgauge-issue-audit"), "1.0.0", true)
	fs, _ := agentSkillsFindingsIn([]agentSkillsHome{home}, []string{root})
	if len(fs) != 1 || fs[0].Evidence["build"] != "2.0.0" {
		t.Fatalf("findings = %s; want drift against the mirror's 2.0.0", findingsText(fs))
	}
}

func TestAgentSkillsHomesHonourEnv(t *testing.T) {
	t.Setenv("CODEX_HOME", "/x/codex")
	t.Setenv("GROK_HOME", "")
	saved := agentSkillsUserHome
	t.Cleanup(func() { agentSkillsUserHome = saved })
	agentSkillsUserHome = func() (string, error) { return "/u", nil }
	homes, err := agentSkillsHomes()
	if err != nil {
		t.Fatal(err)
	}
	if homes[0].skills != "/x/codex/skills" || homes[1].skills != "/u/.grok/skills" {
		t.Fatalf("homes = %v", homes)
	}
}
