package docs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Ensure filepath and os are used.
var _ = filepath.Join
var _ = os.MkdirAll

// mockGitRunner returns a GitRunner that uses a map from absolute path to
// ISO date string. An empty string means "untracked". A special sentinel
// value "ERROR" causes the runner to return an error string.
func mockGitRunner(m map[string]string) func(file string) (string, string) {
	return func(file string) (string, string) {
		v, ok := m[file]
		if !ok {
			return "", "" // unknown file → treat as untracked (empty date, no warning)
		}
		if v == "ERROR" {
			return "", fmt.Sprintf("mock error for %s", file)
		}
		return v, ""
	}
}

func TestCheckFreshness_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(nil),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0", res.FilesScanned)
	}
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0", res.StaleCount)
	}
}

func TestCheckFreshness_NoUpdatedLine(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Hello\n\nNo updated metadata here.\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-01"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.FilesWithUpdatedMetadata != 0 {
		t.Errorf("FilesWithUpdatedMetadata = %d, want 0", res.FilesWithUpdatedMetadata)
	}
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0", res.StaleCount)
	}
}

func TestCheckFreshness_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "GUIDE.md", "# Guide\n\nUpdated: 2026-04-20\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-15"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesWithUpdatedMetadata != 1 {
		t.Errorf("FilesWithUpdatedMetadata = %d, want 1", res.FilesWithUpdatedMetadata)
	}
	// git date (Apr 15) < documented date (Apr 20) → fresh
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0 (file is fresh)", res.StaleCount)
	}
}

func TestCheckFreshness_StaleFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "SETUP.md", "# Setup\n\nUpdated: 2026-01-15\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-20"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1", res.StaleCount)
	}
	f := res.StaleFindings[0]
	if f.DocumentedDate != "2026-01-15" {
		t.Errorf("DocumentedDate = %q, want 2026-01-15", f.DocumentedDate)
	}
	if f.GitDate != "2026-04-20" {
		t.Errorf("GitDate = %q, want 2026-04-20", f.GitDate)
	}
	if f.DaysStale != 95 {
		t.Errorf("DaysStale = %d, want 95", f.DaysStale)
	}
}

func TestCheckFreshness_MultipleStaleFiles(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "A.md", "Updated: 2025-12-01\n")
	p2 := writeFile(t, dir, "B.md", "Updated: 2025-11-01\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root: dir,
		GitRunner: mockGitRunner(map[string]string{
			p1: "2026-01-10",
			p2: "2026-02-01",
		}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StaleCount != 2 {
		t.Errorf("StaleCount = %d, want 2", res.StaleCount)
	}
}

func TestCheckFreshness_UntrackedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "NEW.md", "Updated: 2026-04-01\n")

	// GitRunner returns empty string → file is untracked.
	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0 (untracked file skipped)", res.StaleCount)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for untracked file")
	}
}

func TestCheckFreshness_MalformedDate(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "BAD.md", "Updated: not-a-date\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-01"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Malformed date results in no finding and a warning.
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0", res.StaleCount)
	}
	// The "Updated: not-a-date" pattern won't even match updatedRe (requires YYYY-MM-DD format)
	// so no warning is expected here — this is correct behaviour.
}

func TestCheckFreshness_UpdatedInCodeFence(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "CODE.md", "Example:\n```\nUpdated: 2025-01-01\n```\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-20"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Updated: inside a code fence should be skipped.
	if res.FilesWithUpdatedMetadata != 0 {
		t.Errorf("FilesWithUpdatedMetadata = %d, want 0 (Updated inside fence should be skipped)", res.FilesWithUpdatedMetadata)
	}
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0", res.StaleCount)
	}
}

func TestCheckFreshness_BoldUpdatedPattern(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "DOCS.md", "**Updated**: 2026-01-10\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-28"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesWithUpdatedMetadata != 1 {
		t.Errorf("FilesWithUpdatedMetadata = %d, want 1", res.FilesWithUpdatedMetadata)
	}
	if res.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1", res.StaleCount)
	}
}

func TestCheckFreshness_TableUpdatedPattern(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "TABLE.md", "| Field | Value |\n| Updated | 2026-02-01 |\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-28"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesWithUpdatedMetadata != 1 {
		t.Errorf("FilesWithUpdatedMetadata = %d, want 1", res.FilesWithUpdatedMetadata)
	}
	if res.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1", res.StaleCount)
	}
}

func TestCheckFreshness_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	nmDir := filepath.Join(dir, "node_modules", "pkg")
	if err := os.MkdirAll(nmDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, nmDir, "README.md", "Updated: 2020-01-01\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(nil),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0 (node_modules skipped)", res.FilesScanned)
	}
}

func TestCheckFreshness_DefaultsToCurrentDir(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Skipf("cannot chdir: %v", err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		GitRunner: mockGitRunner(nil),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Root == "" {
		t.Error("Root should be set when no explicit root given")
	}
}

func TestCheckFreshness_LineNumberRecorded(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "LINE.md", "# Title\n\nSome intro.\n\nUpdated: 2025-06-01\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "2026-04-28"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StaleCount != 1 {
		t.Fatalf("StaleCount = %d, want 1", res.StaleCount)
	}
	if res.StaleFindings[0].Line != 5 {
		t.Errorf("Line = %d, want 5", res.StaleFindings[0].Line)
	}
}

func TestCheckFreshness_GitRunnerError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "ERR.md", "Updated: 2026-01-01\n")

	res, err := CheckFreshness(context.Background(), CheckFreshnessOptions{
		Root:      dir,
		GitRunner: mockGitRunner(map[string]string{path: "ERROR"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Git error produces warning, not stale finding.
	if res.StaleCount != 0 {
		t.Errorf("StaleCount = %d, want 0 (git error should produce warning, not finding)", res.StaleCount)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for git runner error")
	}
}
