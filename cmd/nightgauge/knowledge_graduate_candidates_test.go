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

const fixtureDecisions = `# Decisions: #777

## ADR-001: Always parameterize SQL queries

**Status**: Accepted
**Context**: Hand-rolled SQL leaks data when concatenation is used.
**Decision**: Always parameterize queries. No string concatenation for SQL. Every service that touches storage must use parameter binding.
**Consequences**: Reviewers gain a clear rule, attack surface drops, and code review effort goes down across all data-access paths.

## ADR-002: Specific cache for issue 777
<!-- graduated-to: docs/X.md -->

**Status**: Accepted
**Context**: this PR needs an internal cache in packages/foo/bar.ts.
**Decision**: Add cache at packages/foo/bar.ts.
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`

func writeCLIFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	kbDir := filepath.Join(root, ".nightgauge", "knowledge", "features", "777-cli-test")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "decisions.md"), []byte(fixtureDecisions), 0o644); err != nil {
		t.Fatal(err)
	}
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ARCHITECTURE.md", "CODE_STANDARDS.md", "KNOWLEDGE_BASE.md"} {
		if err := os.WriteFile(filepath.Join(docsDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestKnowledgeGraduateCandidatesCmd_JSON(t *testing.T) {
	root := writeCLIFixture(t)
	cmd := knowledgeGraduateCandidatesCmd()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	// Cobra's RunE writes JSON to os.Stdout directly (matching other knowledge
	// subcommands). Capture it via the same trick as knowledge_telemetry_test:
	// pipe os.Stdout, run, restore.
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	cmd.SetArgs([]string{"777", "--workdir=" + root, "--json"})
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	output, _ := readAll(r)

	if runErr != nil {
		t.Fatalf("execute: %v (stderr=%s)", runErr, stderr.String())
	}

	var result graduation.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, output)
	}
	if result.Issue != 777 {
		t.Errorf("Issue = %d, want 777", result.Issue)
	}
	if len(result.Candidates) == 0 {
		t.Fatalf("expected ADR-001 to qualify, got 0 candidates")
	}
	top := result.Candidates[0]
	if top.ADRIndex != 1 {
		t.Errorf("top.ADRIndex = %d, want 1", top.ADRIndex)
	}
	if top.SuggestedDest == "" {
		t.Errorf("SuggestedDest empty")
	}
}

func TestKnowledgeGraduateCandidatesCmd_HumanReadable_NoCandidates(t *testing.T) {
	root := writeCLIFixture(t)
	cmd := knowledgeGraduateCandidatesCmd()

	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	// Use a very high min-score so nothing qualifies.
	cmd.SetArgs([]string{"777", "--workdir=" + root, "--min-score=100"})
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout
	output, _ := readAll(r)

	if runErr != nil {
		t.Fatalf("execute: %v (stderr=%s)", runErr, stderr.String())
	}
	if !strings.Contains(string(output), "0 graduation candidates") {
		t.Errorf("expected '0 graduation candidates' in output, got: %s", output)
	}
}

func TestKnowledgeGraduateCandidatesCmd_InvalidIssue(t *testing.T) {
	cmd := knowledgeGraduateCandidatesCmd()
	cmd.SetArgs([]string{"notanint"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error for non-numeric issue, got nil")
	}
}

func readAll(r *os.File) ([]byte, error) {
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
	return buf.Bytes(), nil
}
