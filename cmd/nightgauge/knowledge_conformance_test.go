package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

func writeKB(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, ".nightgauge", "knowledge", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runValidate executes the validate command, capturing whatever it prints to
// os.Stdout (the command writes JSON there directly, not to cmd.OutOrStdout).
func runValidate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := knowledgeValidateCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(args)
	err := cmd.Execute()

	_ = w.Close()
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	return string(buf[:n]), err
}

func TestKnowledgeValidateConformance_ExitsNonZeroAndNamesEveryViolation(t *testing.T) {
	root := t.TempDir()
	writeKB(t, root, "features/1-ok/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")
	writeKB(t, root, "features/2-bare/PRD.md", "# PRD\n\nNo block.\n")

	out, err := runValidate(t, "--conformance", "--json", "--workdir", root)
	if err == nil {
		t.Fatal("expected a non-zero exit on a violation")
	}

	// The exit code must follow the verdict even under --json: the JSON branch
	// used to return right after encoding, so every scripted caller saw
	// success no matter what the check found.
	var decoded struct {
		Valid      bool `json:"valid"`
		Violations []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"violations"`
	}
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("decode %q: %v", out, jerr)
	}
	if decoded.Valid {
		t.Error("valid=true alongside a violation")
	}
	if len(decoded.Violations) != 1 || decoded.Violations[0].Path != "features/2-bare/PRD.md" {
		t.Fatalf("violations = %+v", decoded.Violations)
	}
	if decoded.Violations[0].Reason != "no_frontmatter" {
		t.Errorf("reason = %q", decoded.Violations[0].Reason)
	}
}

func TestKnowledgeValidateConformance_CleanBaseExitsZero(t *testing.T) {
	root := t.TempDir()
	writeKB(t, root, "features/1-ok/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")
	writeKB(t, root, "README.md", "# Knowledge Base\n")

	out, err := runValidate(t, "--conformance", "--json", "--workdir", root)
	if err != nil {
		t.Fatalf("clean base should exit zero: %v (%s)", err, out)
	}
	if !strings.Contains(out, `"violations": []`) {
		t.Errorf("expected an empty violations list, got %s", out)
	}
}

func TestKnowledgeValidateConformance_FixMigratesAPreContractBase(t *testing.T) {
	root := t.TempDir()
	writeKB(t, root, "features/7-legacy/PRD.md", "# PRD: #7\n\nWritten before the contract existed.\n")
	writeKB(t, root, "features/7-legacy/decisions.md", "---\ntags: [a]\n---\n\n# Decisions: #7\n")
	writeKB(t, root, "architecture/pattern.md", "# A pattern\n")
	// An unparseable block is reported, never guessed at.
	writeKB(t, root, "features/8-broken/PRD.md", "---\ntype: prd\n bad: [\n---\n\n# PRD\n")

	_, err := runValidate(t, "--conformance", "--fix", "--json", "--workdir", root)
	if err == nil {
		t.Fatal("the unparseable entry should still fail the run after --fix")
	}

	kb := filepath.Join(root, ".nightgauge", "knowledge")
	for rel, wantType := range map[string]string{
		"features/7-legacy/PRD.md":       okf.TypePRD,
		"features/7-legacy/decisions.md": okf.TypeDecisions,
		"architecture/pattern.md":        okf.TypeArchitecture,
	} {
		block, perr := okf.ParseFrontmatterFile(filepath.Join(kb, filepath.FromSlash(rel)))
		if perr != nil {
			t.Fatalf("%s: %v", rel, perr)
		}
		if block == nil || block.Type != wantType {
			t.Errorf("%s type = %+v, want %q", rel, block, wantType)
		}
		if block.Generated == nil || block.Generated.By != "process:knowledge-migrate" {
			t.Errorf("%s generated = %+v", rel, block.Generated)
		}
	}

	// The body survived the migration.
	data, rerr := os.ReadFile(filepath.Join(kb, "features", "7-legacy", "PRD.md"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(data), "Written before the contract existed.") {
		t.Errorf("migration lost the body:\n%s", data)
	}

	// The pre-existing tag survived too.
	block, perr := okf.ParseFrontmatterFile(filepath.Join(kb, "features", "7-legacy", "decisions.md"))
	if perr != nil {
		t.Fatal(perr)
	}
	if len(block.Tags) != 1 || block.Tags[0] != "a" {
		t.Errorf("migration dropped existing frontmatter: %+v", block.Tags)
	}

	// The malformed one was left exactly as it was.
	broken, rerr := os.ReadFile(filepath.Join(kb, "features", "8-broken", "PRD.md"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(broken), " bad: [") {
		t.Errorf("--fix rewrote an unparseable block:\n%s", broken)
	}
}

func TestKnowledgeValidate_RejectsInvalidFlagCombinations(t *testing.T) {
	root := t.TempDir()
	writeKB(t, root, "features/1-ok/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")

	if _, err := runValidate(t, "--workdir", root); err == nil {
		t.Error("no issue number and no --conformance should be an error")
	}
	if _, err := runValidate(t, "1", "--fix", "--workdir", root); err == nil {
		t.Error("--fix on the per-issue path should be an error")
	}
}
