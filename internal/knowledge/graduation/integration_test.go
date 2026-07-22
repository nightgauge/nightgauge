package graduation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge/graduation"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

const integrationDecisions = `# Decisions: #123 — test fixture

## Architecture Decisions

## ADR-001: Always parameterize SQL queries

**Status**: Accepted
**Context**: Hand-rolled SQL leaks data when concatenation is used.
**Decision**: Always parameterize queries. No string concatenation for SQL. Every service that touches storage must use parameter binding.
**Consequences**: Reviewers gain a clear rule, attack surface drops, and code review effort goes down across all data-access paths.

## ADR-002: Inline cache for issue 123

**Status**: Accepted
**Context**: This PR needs an internal cache in packages/foo/bar.ts.
**Decision**: Add an LRU at packages/foo/bar.ts that wraps the response in internal/storage.
**Consequences**: Cache invalidation handled by ttl expiry.

## ADR-003: Already graduated rule
<!-- graduated-to: docs/ARCHITECTURE.md#x -->

**Status**: Accepted
**Context**: Same shape as ADR-001 but graduated.
**Decision**: Apply the same approach via packages/foo/bar.go.
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`

func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// decisions.md under .nightgauge/knowledge/features/123-test/
	knowledgeDir := filepath.Join(root, ".nightgauge", "knowledge", "features", "123-test")
	if err := os.MkdirAll(knowledgeDir, 0o755); err != nil {
		t.Fatalf("mkdir knowledge: %v", err)
	}
	decisionsPath := filepath.Join(knowledgeDir, "decisions.md")
	if err := os.WriteFile(decisionsPath, []byte(integrationDecisions), 0o644); err != nil {
		t.Fatalf("write decisions.md: %v", err)
	}

	// docs/*.md so suggestedDest has something to match.
	docsDir := filepath.Join(root, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	for _, name := range []string{"ARCHITECTURE.md", "CODE_STANDARDS.md", "TESTING.md", "KNOWLEDGE_BASE.md", "security.md"} {
		if err := os.WriteFile(filepath.Join(docsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Seed telemetry: 3 recall_hit events from 3 distinct future issues, all
	// AFTER decisions.md mtime; one from source issue (123) and one from
	// before cutoff to verify filtering.
	historyDir := filepath.Join(root, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	relDecisions := ".nightgauge/knowledge/features/123-test/decisions.md"
	events := []telemetry.Event{
		{Type: telemetry.EventRecallHit, Path: relDecisions, IssueNumber: 200, Timestamp: "2099-01-02T00:00:00Z", Stage: "feature-dev"},
		{Type: telemetry.EventRecallHit, Path: relDecisions, IssueNumber: 201, Timestamp: "2099-01-03T00:00:00Z", Stage: "feature-dev"},
		{Type: telemetry.EventRecallHit, Path: relDecisions, IssueNumber: 202, Timestamp: "2099-01-04T00:00:00Z", Stage: "feature-dev"},
		// Source-issue recall — must be filtered.
		{Type: telemetry.EventRecallHit, Path: relDecisions, IssueNumber: 123, Timestamp: "2099-01-05T00:00:00Z", Stage: "feature-dev"},
		// Before-cutoff event (pre-file mtime is impossible to forge without
		// touching system time; instead emit an EventScaffold for the issue
		// and an old recall_hit prior to it).
		{Type: telemetry.EventScaffold, IssueNumber: 123, Timestamp: "2099-01-01T00:00:00Z", Stage: "issue-pickup"},
		{Type: telemetry.EventRecallHit, Path: relDecisions, IssueNumber: 999, Timestamp: "2025-01-01T00:00:00Z", Stage: "feature-dev"},
	}
	historyFile := filepath.Join(historyDir, "knowledge-events.jsonl")
	f, err := os.Create(historyFile)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	return root
}

func TestCandidates_Integration_StrongCandidateWins(t *testing.T) {
	root := writeFixture(t)
	result, err := graduation.Candidates(root, 123, graduation.Options{})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if result.Issue != 123 {
		t.Errorf("Issue = %d, want 123", result.Issue)
	}
	if len(result.Candidates) == 0 {
		t.Fatalf("got 0 candidates, expected ADR-001 to qualify")
	}
	top := result.Candidates[0]
	if top.ADRIndex != 1 {
		t.Errorf("top.ADRIndex = %d, want 1", top.ADRIndex)
	}
	if top.Score < 4 {
		t.Errorf("top.Score = %d, want >=4 (threshold)", top.Score)
	}
	// Must include the recall_hits signal (3 distinct future issues -> +3).
	if !signalsContainPrefix(top.Signals, "recall_hits:") {
		t.Errorf("top.Signals = %v, want to include recall_hits:N", top.Signals)
	}
	if !signalsContain(top.Signals, "general_language") {
		t.Errorf("top.Signals = %v, want general_language", top.Signals)
	}
	if !signalsContain(top.Signals, "pattern_language") {
		t.Errorf("top.Signals = %v, want pattern_language", top.Signals)
	}
	if !signalsContain(top.Signals, "filled_consequences") {
		t.Errorf("top.Signals = %v, want filled_consequences", top.Signals)
	}
	if top.SuggestedDest == "" {
		t.Errorf("SuggestedDest is empty")
	}

	// ADR-002 (file-path-heavy + sparse consequences + issue-specific title) and
	// ADR-003 (already-graduated) must be filtered out by the threshold.
	for _, c := range result.Candidates {
		if c.ADRIndex == 2 {
			t.Errorf("ADR-002 unexpectedly qualified at score %d", c.Score)
		}
		if c.ADRIndex == 3 {
			t.Errorf("ADR-003 (already graduated) unexpectedly qualified at score %d", c.Score)
		}
	}
}

func TestCandidates_NoTelemetryFile(t *testing.T) {
	root := writeFixture(t)
	// Remove the telemetry file - should still score on structural signals.
	_ = os.Remove(filepath.Join(root, ".nightgauge", "pipeline", "history", "knowledge-events.jsonl"))

	result, err := graduation.Candidates(root, 123, graduation.Options{})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	// ADR-001 still has general_language + pattern_language + filled_consequences = +5.
	if len(result.Candidates) == 0 {
		t.Fatal("expected ADR-001 to qualify even without telemetry")
	}
}

func TestCandidates_MissingIssue(t *testing.T) {
	root := t.TempDir()
	_, err := graduation.Candidates(root, 999, graduation.Options{})
	if err == nil {
		t.Error("expected error for missing decisions.md, got nil")
	}
}

func TestCandidates_MinScoreOverride(t *testing.T) {
	root := writeFixture(t)
	// Raise threshold above what any ADR could achieve.
	result, err := graduation.Candidates(root, 123, graduation.Options{MinScore: 100})
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Errorf("got %d candidates with MinScore=100, want 0", len(result.Candidates))
	}
}

func signalsContain(signals []string, want string) bool {
	for _, s := range signals {
		if s == want {
			return true
		}
	}
	return false
}

func signalsContainPrefix(signals []string, prefix string) bool {
	for _, s := range signals {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
