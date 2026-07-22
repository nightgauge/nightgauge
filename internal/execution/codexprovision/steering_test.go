package codexprovision

import (
	"path/filepath"
	"strings"
	"testing"
)

// --- extractSummary ---

func TestExtractSummary_StopsAtSecondHeader(t *testing.T) {
	// extractSummary keeps the first section only: it stops at the SECOND header
	// of any level (## First here), so the intro under # Title is kept but every
	// later section is dropped.
	content := "# Title\nintro line\n## First\nbody\n## Second\nshould not appear"
	got := extractSummary(content, 50)
	if !strings.Contains(got, "# Title") || !strings.Contains(got, "intro line") {
		t.Errorf("summary dropped the first section: %q", got)
	}
	if strings.Contains(got, "## First") || strings.Contains(got, "Second") {
		t.Errorf("summary should stop at the second header of any level: %q", got)
	}
}

func TestExtractSummary_CapsAtMaxLines(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# One\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("filler\n")
	}
	got := extractSummary(sb.String(), 5)
	if lines := strings.Count(got, "\n") + 1; lines > 5 {
		t.Errorf("summary exceeded maxLines: %d lines", lines)
	}
}

// --- assembleSteeringContent ---

func TestAssembleSteeringContent_AlwaysHasHeaderAndRules(t *testing.T) {
	got := assembleSteeringContent(t.TempDir()) // empty project — no source docs
	assertContains(t, got, "# Nightgauge Pipeline Steering (Codex)")
	assertContains(t, got, "## Key Rules")
	assertContains(t, got, "Never push directly to main")
	assertContains(t, got, "Never hardcode secrets")
}

func TestAssembleSteeringContent_IncludesProjectAndStandards(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# My Project\nDoes a thing.\n")
	writeFile(t, filepath.Join(dir, "standards", "code-standards.md"), "# Standards\nUse tabs.\n")
	writeFile(t, filepath.Join(dir, "standards", "security.md"), "# Security\nNo secrets.\n")
	writeFile(t, filepath.Join(dir, "docs", "GIT_WORKFLOW.md"), "# Git\nBranch first.\n")

	got := assembleSteeringContent(dir)
	assertContains(t, got, "## Project")
	assertContains(t, got, "My Project")
	assertContains(t, got, "## Coding Standards")
	assertContains(t, got, "Use tabs.")
	assertContains(t, got, "## Security")
	assertContains(t, got, "## Git Workflow")
}

func TestReadProjectDescription_StripsManagedBlockFromAgentsMd(t *testing.T) {
	dir := t.TempDir()
	// No CLAUDE.md → falls back to AGENTS.md, but the managed block must be ignored.
	agents := "# User Project Notes\nReal description.\n\n" +
		steeringManagedBegin + "\n# generated junk\n" + steeringManagedEnd + "\n"
	writeFile(t, filepath.Join(dir, "AGENTS.md"), agents)

	got := readProjectDescription(dir)
	assertContains(t, got, "Real description.")
	if strings.Contains(got, "generated junk") {
		t.Errorf("managed block must be stripped before reading AGENTS.md: %q", got)
	}
}

// --- upsert / strip managed steering block ---

func TestUpsertManagedSteeringBlock_FreshFile(t *testing.T) {
	got := upsertManagedSteeringBlock("", false, "INNER")
	want := steeringManagedBegin + "\nINNER\n" + steeringManagedEnd + "\n"
	if got != want {
		t.Errorf("fresh upsert = %q, want %q", got, want)
	}
}

func TestUpsertManagedSteeringBlock_PreservesUserContent(t *testing.T) {
	existing := "# User Heading\nKeep me.\n"
	got := upsertManagedSteeringBlock(existing, true, "INNER")
	assertContains(t, got, "# User Heading")
	assertContains(t, got, "Keep me.")
	assertContains(t, got, steeringManagedBegin)
	assertContains(t, got, "INNER")
	// User content must come before the appended managed block.
	if strings.Index(got, "Keep me.") > strings.Index(got, steeringManagedBegin) {
		t.Errorf("user content should precede the managed block:\n%s", got)
	}
}

func TestUpsertManagedSteeringBlock_ReplacesExistingBlock(t *testing.T) {
	existing := "before\n\n" + steeringManagedBegin + "\nOLD\n" + steeringManagedEnd + "\n\nafter\n"
	got := upsertManagedSteeringBlock(existing, true, "NEW")
	assertContains(t, got, "before")
	assertContains(t, got, "after")
	assertContains(t, got, "NEW")
	if strings.Contains(got, "OLD") {
		t.Errorf("old managed content should be replaced:\n%s", got)
	}
	if strings.Count(got, steeringManagedBegin) != 1 {
		t.Errorf("want exactly one managed block:\n%s", got)
	}
}

func TestUpsertManagedSteeringBlock_Idempotent(t *testing.T) {
	first := upsertManagedSteeringBlock("user stuff\n", true, "INNER")
	second := upsertManagedSteeringBlock(first, true, "INNER")
	if first != second {
		t.Errorf("steering upsert not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestStripManagedSteeringBlock(t *testing.T) {
	existing := "keep before\n\n" + steeringManagedBegin + "\nGEN\n" + steeringManagedEnd + "\n\nkeep after\n"
	got := stripManagedSteeringBlock(existing)
	assertContains(t, got, "keep before")
	assertContains(t, got, "keep after")
	if strings.Contains(got, steeringManagedBegin) || strings.Contains(got, "GEN") {
		t.Errorf("strip left managed remnants:\n%s", got)
	}
}

func TestStripManagedSteeringBlock_NoBlockReturnsUnchanged(t *testing.T) {
	existing := "just user content\n"
	if got := stripManagedSteeringBlock(existing); got != existing {
		t.Errorf("strip with no block changed content: %q", got)
	}
}

func TestIsOnlyManagedSteeringChange(t *testing.T) {
	managed := steeringManagedBegin + "\ngenerated\n" + steeringManagedEnd + "\n"
	if !IsOnlyManagedSteeringChange("# User guidance\n", "# User guidance\n\n"+managed) {
		t.Fatal("generated managed block should not count as user-authored change")
	}
	if IsOnlyManagedSteeringChange("# User guidance\n", "# Changed guidance\n\n"+managed) {
		t.Fatal("user-authored change outside managed block must be preserved")
	}
}

// --- computeNextAgentsMd: end-to-end ---

func TestComputeNextAgentsMd_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "# Proj\nDesc.\n")
	first := computeNextAgentsMd("", false, dir)
	second := computeNextAgentsMd(first, true, dir)
	if first != second {
		t.Errorf("computeNextAgentsMd not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	assertContains(t, first, steeringManagedBegin)
	assertContains(t, first, "Proj")
}
