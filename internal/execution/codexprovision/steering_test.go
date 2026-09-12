package codexprovision

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- extractSummary ---

func TestExtractSummary_H1ThenH2KeepsTheSectionBody(t *testing.T) {
	// issue 1675: the old reader stopped at the SECOND H1/H2, so a title immediately
	// followed by a sub-heading summarised to the title alone.
	content := "# Project\n## Overview\nIt does X.\n## Build\nrun make\n"
	got := extractSummary(content, 50)
	for _, want := range []string{"# Project", "## Overview", "It does X.", "## Build", "run make"} {
		assertContains(t, got, want)
	}
	if got == "# Project" {
		t.Fatalf("summary collapsed to the title: %q", got)
	}
}

func TestExtractSummary_DropsHeadingsWithNoBody(t *testing.T) {
	got := extractSummary("# T\n\n## Empty\n\n## Real\nbody\n## Tail\n", 50)
	if strings.Contains(got, "Empty") || strings.Contains(got, "Tail") {
		t.Errorf("bodiless headings must be dropped: %q", got)
	}
	assertContains(t, got, "## Real\nbody")
}

// TestExtractSummary_SharedFixtures pins the Go reader to the byte-exact
// goldens the TypeScript steeringSources test also reads, so the two dispatch
// paths cannot drift (issue 1675). Regenerate with
// NIGHTGAUGE_UPDATE_GOLDEN=1 go test ./internal/execution/codexprovision/.
func TestExtractSummary_SharedFixtures(t *testing.T) {
	dir := filepath.Join("testdata", "extract-summary")
	raw, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string `json:"name"`
		MaxLines int    `json:"maxLines"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, c.Name+".input"))
			if err != nil {
				t.Fatal(err)
			}
			got := extractSummary(string(in), c.MaxLines)
			goldenPath := filepath.Join(dir, c.Name+".golden")
			if os.Getenv("NIGHTGAUGE_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("summary drifted from golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
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

func TestReadProjectDescription_PrefersAgentsMdOverClaudeAdapter(t *testing.T) {
	// issue 1675: CLAUDE.md is a thin `@AGENTS.md` adapter; steering must summarise
	// the canonical contract, not the adapter.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "# Contract\n## Scope\nThe real rules.\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "@AGENTS.md\n\n# Claude Code adapter\nClaude-only notes.\n")
	got := readProjectDescription(dir)
	assertContains(t, got, "The real rules.")
	if strings.Contains(got, "Claude-only") {
		t.Errorf("adapter text leaked into the description: %q", got)
	}
}

func TestReadProjectDescription_FallsBackToClaudeSkippingImport(t *testing.T) {
	dir := t.TempDir()
	// AGENTS.md holding only the managed block has no user part.
	writeFile(t, filepath.Join(dir, "AGENTS.md"), steeringManagedBegin+"\ngen\n"+steeringManagedEnd+"\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "\n@AGENTS.md\n\n# Legacy\nOld-model description.\n")
	got := readProjectDescription(dir)
	if strings.Contains(got, "@AGENTS.md") {
		t.Errorf("the import line is not a description: %q", got)
	}
	assertContains(t, got, "Old-model description.")
}

func TestReadProjectDescription_ImportOnlyClaudeIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "@AGENTS.md\n")
	if got := readProjectDescription(dir); got != "" {
		t.Errorf("import-only CLAUDE.md must yield no description, got %q", got)
	}
}

func TestReadProjectDescription_StripsManagedBlockFromAgentsMd(t *testing.T) {
	dir := t.TempDir()
	// The managed block must be ignored when reading AGENTS.md.
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
