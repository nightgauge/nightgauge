package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeKnowledgeFixture(t *testing.T, root string, issueNumber int, slug string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "knowledge", "features",
		fmt.Sprintf("%d-%s", issueNumber, slug))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestRenderPRSection_PRDAndDecisions(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 42, "test-feature", map[string]string{
		"PRD.md":       "# PRD\n",
		"decisions.md": "# Decisions\n",
	})

	got, err := RenderPRSection(root, 42)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	want := "## Knowledge\n\n" +
		"- [PRD](.nightgauge/knowledge/features/42-test-feature/PRD.md) — Product requirements and design decisions\n" +
		"- [Decisions](.nightgauge/knowledge/features/42-test-feature/decisions.md) — Architecture and implementation decisions\n"
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderPRSection_PRDOnly(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 100, "only-prd", map[string]string{
		"PRD.md": "# PRD\n",
	})

	got, err := RenderPRSection(root, 100)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	want := "## Knowledge\n\n" +
		"- [PRD](.nightgauge/knowledge/features/100-only-prd/PRD.md) — Product requirements and design decisions\n"
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderPRSection_GenericTitleCase(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 7, "generic", map[string]string{
		"something-custom.md": "# something\n",
	})

	got, err := RenderPRSection(root, 7)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	want := "## Knowledge\n\n" +
		"- [Something Custom](.nightgauge/knowledge/features/7-generic/something-custom.md)\n"
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderPRSection_MixedWellKnownAndGeneric(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 9, "mixed", map[string]string{
		"PRD.md":            "# PRD\n",
		"decisions.md":      "# Decisions\n",
		"research_notes.md": "# Research\n",
		"benchmarks.md":     "# Benchmarks\n",
	})

	got, err := RenderPRSection(root, 9)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	// Well-known first (PRD, then Decisions); remaining sorted case-insensitively
	// (benchmarks.md before research_notes.md).
	want := "## Knowledge\n\n" +
		"- [PRD](.nightgauge/knowledge/features/9-mixed/PRD.md) — Product requirements and design decisions\n" +
		"- [Decisions](.nightgauge/knowledge/features/9-mixed/decisions.md) — Architecture and implementation decisions\n" +
		"- [Benchmarks](.nightgauge/knowledge/features/9-mixed/benchmarks.md)\n" +
		"- [Research Notes](.nightgauge/knowledge/features/9-mixed/research_notes.md)\n"
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderPRSection_OnlyScaffoldingFiles(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 11, "scaffold-only", map[string]string{
		"README.md":    "# README\n",
		"_template.md": "# template\n",
	})

	got, err := RenderPRSection(root, 11)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output for scaffolding-only directory, got: %q", got)
	}
}

func TestRenderPRSection_MissingDirectory(t *testing.T) {
	root := t.TempDir()
	got, err := RenderPRSection(root, 999)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty output for missing directory, got: %q", got)
	}
}

func TestRenderPRSection_NestedFilesIgnored(t *testing.T) {
	root := t.TempDir()
	dir := writeKnowledgeFixture(t, root, 13, "nested", map[string]string{
		"PRD.md": "# PRD\n",
	})
	// Add a nested directory containing a stray .md file — must be ignored.
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "stray.md"), []byte("# stray\n"), 0o644); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	got, err := RenderPRSection(root, 13)
	if err != nil {
		t.Fatalf("RenderPRSection: %v", err)
	}
	want := "## Knowledge\n\n" +
		"- [PRD](.nightgauge/knowledge/features/13-nested/PRD.md) — Product requirements and design decisions\n"
	if got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderPRSection_InvalidIssue(t *testing.T) {
	root := t.TempDir()
	if _, err := RenderPRSection(root, 0); err == nil {
		t.Errorf("expected error for issue=0")
	}
	if _, err := RenderPRSection(root, -5); err == nil {
		t.Errorf("expected error for negative issue")
	}
}

func TestTitleCaseFromFilename(t *testing.T) {
	tests := []struct{ in, want string }{
		{"PRD.md", "PRD"},
		{"decisions.md", "Decisions"},
		{"my-notes.md", "My Notes"},
		{"complex_test_file.md", "Complex Test File"},
		{"already-Mixed-Case.md", "Already Mixed Case"},
		{"single.md", "Single"},
		{"a-b-c.md", "A B C"},
	}
	for _, tc := range tests {
		got := titleCaseFromFilename(tc.in)
		if got != tc.want {
			t.Errorf("titleCaseFromFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
