package knowledge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

const sampleDecisions = `# Decisions: #42 — Add photo upload

## Architecture Decisions

## ADR-001: Use SSE over WebSockets

**Status**: Accepted
**Context**: Pipeline events are server-push only.
**Decision**: SSE keeps infra simple.
**Consequences**: No bidirectional channel.

## ADR-002: KnowledgePath nullability

**Status**: Accepted
**Context**: Some issues have empty bodies.
**Decision**: Set KnowledgePath=null for empty.
**Consequences**: Skill must handle null.
`

func writeDecisions(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestWriteBacklink_AppendsAfterHeading(t *testing.T) {
	path := writeDecisions(t, sampleDecisions)

	err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: path,
		ADRAnchor:     "ADR-001",
		DocsSection:   "docs/ARCHITECTURE.md#sse-pipeline-events",
	})
	if err != nil {
		t.Fatalf("WriteBacklink: %v", err)
	}

	got, _ := os.ReadFile(path)
	want := "## ADR-001: Use SSE over WebSockets\n<!-- graduated-to: docs/ARCHITECTURE.md#sse-pipeline-events -->\n"
	if !strings.Contains(string(got), want) {
		t.Errorf("backlink not inserted directly after heading.\nFile contents:\n%s", got)
	}

	// Other ADR block must remain untouched.
	if strings.Count(string(got), "<!-- graduated-to:") != 1 {
		t.Errorf("expected exactly one graduated-to comment, got %d", strings.Count(string(got), "<!-- graduated-to:"))
	}
}

func TestWriteBacklink_Idempotent(t *testing.T) {
	path := writeDecisions(t, sampleDecisions)
	in := knowledge.GraduateInput{
		DecisionsPath: path,
		ADRAnchor:     "adr-001",
		DocsSection:   "docs/ARCHITECTURE.md#sse",
	}
	if err := knowledge.WriteBacklink(in); err != nil {
		t.Fatalf("first WriteBacklink: %v", err)
	}
	first, _ := os.ReadFile(path)
	if err := knowledge.WriteBacklink(in); err != nil {
		t.Fatalf("second WriteBacklink: %v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("expected idempotent write; file changed on second call")
	}
	if strings.Count(string(second), "<!-- graduated-to:") != 1 {
		t.Errorf("expected exactly one backlink after duplicate writes, got %d", strings.Count(string(second), "<!-- graduated-to:"))
	}
}

func TestWriteBacklink_AnchorAcceptsBareNumber(t *testing.T) {
	path := writeDecisions(t, sampleDecisions)
	err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: path,
		ADRAnchor:     "2",
		DocsSection:   "docs/CODE_STANDARDS.md#null-handling",
	})
	if err != nil {
		t.Fatalf("WriteBacklink with bare number: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "## ADR-002: KnowledgePath nullability\n<!-- graduated-to: docs/CODE_STANDARDS.md#null-handling -->") {
		t.Errorf("bare-number anchor did not match ADR-002.\n%s", got)
	}
}

func TestWriteBacklink_AnchorNotFound(t *testing.T) {
	path := writeDecisions(t, sampleDecisions)
	err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: path,
		ADRAnchor:     "ADR-099",
		DocsSection:   "docs/ARCHITECTURE.md#x",
	})
	if err == nil {
		t.Fatal("expected error for missing ADR anchor, got nil")
	}
	if !strings.Contains(err.Error(), "ADR-099") {
		t.Errorf("error should mention the missing anchor; got: %v", err)
	}
}

func TestWriteBacklink_FileMissing(t *testing.T) {
	err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: filepath.Join(t.TempDir(), "nope.md"),
		ADRAnchor:     "ADR-001",
		DocsSection:   "docs/x.md#y",
	})
	if err == nil {
		t.Fatal("expected error for missing decisions.md, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found'; got: %v", err)
	}
}

func TestWriteBacklink_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		in   knowledge.GraduateInput
	}{
		{"missing decisions path", knowledge.GraduateInput{ADRAnchor: "ADR-1", DocsSection: "x"}},
		{"missing anchor", knowledge.GraduateInput{DecisionsPath: "f", DocsSection: "x"}},
		{"missing docs section", knowledge.GraduateInput{DecisionsPath: "f", ADRAnchor: "ADR-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := knowledge.WriteBacklink(tc.in); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestWriteBacklink_StopsAtNextHeading(t *testing.T) {
	// Verify the backlink lands inside ADR-001 and not bleeds into ADR-002.
	path := writeDecisions(t, sampleDecisions)
	if err := knowledge.WriteBacklink(knowledge.GraduateInput{
		DecisionsPath: path,
		ADRAnchor:     "ADR-001",
		DocsSection:   "docs/ARCHITECTURE.md#sse",
	}); err != nil {
		t.Fatalf("WriteBacklink: %v", err)
	}
	got, _ := os.ReadFile(path)
	adr1Start := strings.Index(string(got), "## ADR-001")
	adr2Start := strings.Index(string(got), "## ADR-002")
	backlinkPos := strings.Index(string(got), "<!-- graduated-to:")
	if backlinkPos < adr1Start || backlinkPos > adr2Start {
		t.Errorf("backlink at offset %d not within ADR-001 block (%d..%d)", backlinkPos, adr1Start, adr2Start)
	}
}

func TestFormatGraduatedToComment(t *testing.T) {
	got := knowledge.FormatGraduatedToComment("docs/ARCHITECTURE.md#sse-pipeline-events")
	want := "<!-- graduated-to: docs/ARCHITECTURE.md#sse-pipeline-events -->"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatGraduatedFromComment(t *testing.T) {
	got := knowledge.FormatGraduatedFromComment(".nightgauge/knowledge/features/42-foo/decisions.md", "ADR-3")
	want := "<!-- graduated-from: .nightgauge/knowledge/features/42-foo/decisions.md#adr-003 -->"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestReadADRBlock(t *testing.T) {
	path := writeDecisions(t, sampleDecisions)
	block, err := knowledge.ReadADRBlock(path, "ADR-001")
	if err != nil {
		t.Fatalf("ReadADRBlock: %v", err)
	}
	if !strings.HasPrefix(block, "## ADR-001:") {
		t.Errorf("block should start with ADR-001 heading, got:\n%s", block)
	}
	if strings.Contains(block, "ADR-002") {
		t.Errorf("block should not include ADR-002")
	}
}

func TestFindDecisionsPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "knowledge", "features", "42-add-photo-upload")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "decisions.md"), []byte("# Decisions"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := knowledge.FindDecisionsPath(root, 42)
	if err != nil {
		t.Fatalf("FindDecisionsPath: %v", err)
	}
	want := filepath.Join(".nightgauge", "knowledge", "features", "42-add-photo-upload", "decisions.md")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}

	if _, err := knowledge.FindDecisionsPath(root, 999); err == nil {
		t.Error("expected error for missing issue, got nil")
	}
}

const sampleDecisionsForEnumerate = `# Decisions: #42

## Architecture Decisions

## ADR-001: Strong candidate

**Status**: Proposed
**Context**: AC awards +3 to recall-heavy ADRs.
**Decision**: Always prefer general language. No file path references.
**Consequences**: Reviewers get unambiguous candidates. Threshold >=4 prevents false positives.

## ADR-002: Weak with file paths
<!-- graduated-to: docs/ARCHITECTURE.md#x -->

**Status**: Proposed
**Context**: Lives in packages/foo/bar.ts.
**Decision**: Mutate the file at packages/nightgauge-vscode/src/x.ts.
**Consequences**: replace this placeholder
**Tags**: vscode, extension

## ADR-003: Title for issue #3500

**Status**: Accepted
**Context**: Issue-specific noun in title.
**Decision**: Hard-coded for this PR only.
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`

func TestEnumerateADRBlocks_ParsesAllBlocks(t *testing.T) {
	path := writeDecisions(t, sampleDecisionsForEnumerate)

	blocks, err := knowledge.EnumerateADRBlocks(path)
	if err != nil {
		t.Fatalf("EnumerateADRBlocks: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}

	if blocks[0].Index != 1 || blocks[0].Title != "Strong candidate" {
		t.Errorf("ADR[0] = (%d,%q), want (1, %q)", blocks[0].Index, blocks[0].Title, "Strong candidate")
	}
	if blocks[0].Status != "Proposed" {
		t.Errorf("ADR[0].Status = %q, want Proposed", blocks[0].Status)
	}
	if !strings.Contains(blocks[0].Decision, "Always prefer general language") {
		t.Errorf("ADR[0].Decision missing keyword: %q", blocks[0].Decision)
	}
	if blocks[0].Graduated {
		t.Errorf("ADR[0].Graduated = true, want false")
	}

	if !blocks[1].Graduated {
		t.Errorf("ADR[1].Graduated = false, want true (has graduated-to marker)")
	}
	if blocks[1].Tags == "" || !strings.Contains(blocks[1].Tags, "vscode") {
		t.Errorf("ADR[1].Tags = %q, want to contain 'vscode'", blocks[1].Tags)
	}

	if blocks[2].Index != 3 {
		t.Errorf("ADR[2].Index = %d, want 3", blocks[2].Index)
	}
}

func TestEnumerateADRBlocks_NoFile(t *testing.T) {
	_, err := knowledge.EnumerateADRBlocks(filepath.Join(t.TempDir(), "missing.md"))
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestEnumerateADRBlocks_NoADRHeadings(t *testing.T) {
	path := writeDecisions(t, "# Decisions\n\nNothing here.\n")
	blocks, err := knowledge.EnumerateADRBlocks(path)
	if err != nil {
		t.Fatalf("EnumerateADRBlocks: %v", err)
	}
	if len(blocks) != 0 {
		t.Errorf("got %d blocks, want 0", len(blocks))
	}
}
