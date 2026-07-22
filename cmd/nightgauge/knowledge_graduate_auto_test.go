package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/graduation"
)

// autoFixtureDecisions mirrors the package-internal fixture but lives in the
// CLI tests because the CLI cannot import unexported test fixtures from
// graduation.
const autoCLIDecisions = `# Decisions: #5555

## ADR-001: Always validate input at API boundaries

**Status**: Accepted
**Context**: Every public handler that accepts untrusted bytes must validate.
**Decision**: Always validate inputs at API boundaries. No service trusts upstream services for shape, length, or charset. Every handler that accepts untrusted bytes MUST validate before logging or persisting.
**Consequences**: All handlers share one validation pattern, attack surface drops, and reviewers can grep for the helper across services to audit coverage.
`

// writeAutoCLIFixture builds a temp workspace seeded with a single qualifying
// ADR and several docs/*.md candidates. Returns the workspace root.
func writeAutoCLIFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	kbDir := filepath.Join(root, ".nightgauge", "knowledge", "features", "5555-auto-cli")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "decisions.md"), []byte(autoCLIDecisions), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ARCHITECTURE.md", "CODE_STANDARDS.md", "KNOWLEDGE_BASE.md"} {
		if err := os.WriteFile(filepath.Join(docs, n), []byte("# "+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestKnowledgeGraduateCmd_AutoFlagWiring(t *testing.T) {
	cmd := knowledgeGraduateCmd()
	for _, name := range []string{"auto", "adr-index", "dry-run", "all-candidates", "base", "forge", "repo"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s is not wired on knowledge graduate", name)
		}
	}
}

func TestKnowledgeGraduateCmd_AutoDryRunJSON(t *testing.T) {
	root := writeAutoCLIFixture(t)
	cmd := knowledgeGraduateCmd()

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	cmd.SetArgs([]string{
		"5555",
		"--auto",
		"--dry-run",
		"--json",
		"--workdir=" + root,
	})
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	output := readPipe(t, r)

	if runErr != nil {
		t.Fatalf("execute: %v (stderr=%s)", runErr, stderr.String())
	}

	var res graduation.AutoGraduateResult
	if err := json.Unmarshal(output, &res); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, output)
	}
	if res.Issue != 5555 {
		t.Errorf("Issue = %d, want 5555", res.Issue)
	}
	if res.Status != graduation.AutoStatusDryRun {
		t.Errorf("Status = %q, want dry_run", res.Status)
	}
	if !res.DryRun {
		t.Errorf("DryRun = false, want true")
	}
	if len(res.PerCandidate) != 1 {
		t.Fatalf("PerCandidate = %d, want 1", len(res.PerCandidate))
	}
	o := res.PerCandidate[0]
	if o.Branch != "docs/graduate-5555-adr-001" {
		t.Errorf("Branch = %q", o.Branch)
	}
	if !strings.Contains(o.PlannedAppend, "**Decision**: Always validate inputs at API boundaries.") {
		t.Errorf("PlannedAppend missing verbatim Decision")
	}
	// Filesystem must not have been mutated.
	src, _ := os.ReadFile(filepath.Join(root, ".nightgauge", "knowledge", "features", "5555-auto-cli", "decisions.md"))
	if strings.Contains(string(src), "graduated-to:") {
		t.Errorf("dry-run wrote graduated-to marker to source")
	}
}

func TestKnowledgeGraduateCmd_ManualPathStillWorks(t *testing.T) {
	root := writeAutoCLIFixture(t)
	cmd := knowledgeGraduateCmd()

	// EDITOR unset so the command prints the path and returns without
	// spawning a child process.
	t.Setenv("EDITOR", "")

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	cmd.SetArgs([]string{
		"5555",
		"--section=docs/CODE_STANDARDS.md#always-validate",
		"--adr=ADR-001",
		"--workdir=" + root,
	})
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	output := readPipe(t, r)

	if runErr != nil {
		t.Fatalf("execute: %v (stderr=%s)", runErr, stderr.String())
	}
	if !strings.Contains(string(output), "Backlink written to") {
		t.Errorf("manual path output missing 'Backlink written to': %s", output)
	}
	// Manual path must write the graduated-to marker to source.
	src, _ := os.ReadFile(filepath.Join(root, ".nightgauge", "knowledge", "features", "5555-auto-cli", "decisions.md"))
	if !strings.Contains(string(src), "<!-- graduated-to: docs/CODE_STANDARDS.md#always-validate -->") {
		t.Errorf("manual path did not write graduated-to marker")
	}
}

func TestKnowledgeGraduateCmd_AutoRequiresNoSectionAdr(t *testing.T) {
	// In --auto mode, section/adr are not required. Confirm the dispatch
	// branch is hit and does not error on missing section/adr.
	root := writeAutoCLIFixture(t)
	cmd := knowledgeGraduateCmd()

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	cmd.SetArgs([]string{
		"5555",
		"--auto",
		"--dry-run",
		"--workdir=" + root,
	})
	cmd.SetErr(&bytes.Buffer{})
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = origStdout
	output := readPipe(t, r)

	if runErr != nil {
		t.Fatalf("auto dry-run errored without section/adr: %v", runErr)
	}
	if !strings.Contains(string(output), "status=dry_run") {
		t.Errorf("expected human output with status=dry_run, got: %s", output)
	}
}

func TestKnowledgeGraduateCmd_AutoNoCandidatesReturnsError(t *testing.T) {
	root := t.TempDir()
	// Build a workspace with an empty decisions.md so no candidates exist.
	kbDir := filepath.Join(root, ".nightgauge", "knowledge", "features", "5555-empty")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "decisions.md"), []byte("# nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := knowledgeGraduateCmd()
	cmd.SetArgs([]string{
		"5555",
		"--auto",
		"--dry-run",
		"--workdir=" + root,
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for no_candidates, got nil")
	}
}

func readPipe(t *testing.T, r *os.File) []byte {
	t.Helper()
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes()
}
