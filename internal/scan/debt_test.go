package scan

import (
	"context"
	"strings"
	"testing"
)

func runDebtScan(t *testing.T, dir string) *DebtScanResult {
	t.Helper()
	res, err := RunDebtScan(context.Background(), DebtOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunDebtScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

// TestRunDebtScan_EmptyWorkdir asserts the schema contract: even with no
// matches every consumer's `jq -r '.markers.todo'` must resolve to a number,
// never null. Markers is a fixed-shape struct so all four keys plus total are
// always populated by encoding/json — verify zeroes here too.
func TestRunDebtScan_EmptyWorkdir(t *testing.T) {
	dir := t.TempDir()
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 0 || res.Markers.FIXME != 0 ||
		res.Markers.HACK != 0 || res.Markers.XXX != 0 {
		t.Errorf("markers should be zero on empty workdir: %+v", res.Markers)
	}
	if res.Markers.Total != 0 {
		t.Errorf("total = %d on empty workdir, want 0", res.Markers.Total)
	}
	if res.Files != 0 {
		t.Errorf("files = %d on empty workdir, want 0", res.Files)
	}
}

func TestRunDebtScan_SingleTODOInTS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.ts", "// TODO: refactor\nconst x = 1;\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 1 {
		t.Errorf("todo = %d, want 1", res.Markers.TODO)
	}
	if res.Markers.Total != 1 {
		t.Errorf("total = %d, want 1", res.Markers.Total)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1", res.Files)
	}
}

// Each marker increments at most once per line (line-count semantics). A line
// containing both TODO and FIXME counts as 1 in todo and 1 in fixme — total 2,
// files 1. Documents the boundary against future drift to occurrence-count.
func TestRunDebtScan_MultipleMarkersOnSameLine(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "// TODO FIXME both on this line\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 1 {
		t.Errorf("todo = %d, want 1", res.Markers.TODO)
	}
	if res.Markers.FIXME != 1 {
		t.Errorf("fixme = %d, want 1", res.Markers.FIXME)
	}
	if res.Markers.Total != 2 {
		t.Errorf("total = %d, want 2", res.Markers.Total)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1 (one matching file)", res.Files)
	}
}

// Word-boundary regex: TODOIST must NOT match TODO. Documents the boundary
// choice in the verb's `Long` description.
func TestRunDebtScan_WordBoundaryExcludesSubstrings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.ts", "// TODOIST is a product\n// XXXY not a marker\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 0 {
		t.Errorf("todo = %d on TODOIST line, want 0", res.Markers.TODO)
	}
	if res.Markers.XXX != 0 {
		t.Errorf("xxx = %d on XXXY line, want 0", res.Markers.XXX)
	}
}

// Files outside the source-extension allowlist are ignored. The SKILL.md
// `--include` list does not include `.md`, so debt markers in markdown must
// not count.
func TestRunDebtScan_NonAllowlistExtensionIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "TODO this in markdown\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 0 {
		t.Errorf("todo = %d for .md file, want 0 (.md not in allowlist)", res.Markers.TODO)
	}
}

// Walk pruning skips excluded directories. Markers in node_modules must not
// count (otherwise vendored copies would dominate the tally).
func TestRunDebtScan_ExcludedDirsPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/leak.ts", "// TODO inside node_modules\n")
	writeFile(t, dir, "vendor/leak.go", "// FIXME inside vendor\n")
	writeFile(t, dir, ".git/HEAD", "// TODO not actually code\n")
	res := runDebtScan(t, dir)
	if res.Markers.Total != 0 {
		t.Errorf("total = %d, want 0 (excluded dirs should be pruned)", res.Markers.Total)
	}
}

// Each unique marker keyword is counted independently.
func TestRunDebtScan_AllFourMarkersDetected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go",
		"// TODO one\n// FIXME two\n// HACK three\n// XXX four\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 1 || res.Markers.FIXME != 1 ||
		res.Markers.HACK != 1 || res.Markers.XXX != 1 {
		t.Errorf("expected each marker = 1, got %+v", res.Markers)
	}
	if res.Markers.Total != 4 {
		t.Errorf("total = %d, want 4", res.Markers.Total)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1 (single source file with markers)", res.Files)
	}
}

// Multiple files with markers — Files counts files-containing-marker, not
// total marker lines.
func TestRunDebtScan_FilesCountIsFilesNotLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.ts", "// TODO\n// TODO again\n// TODO third\n")
	writeFile(t, dir, "b.ts", "// FIXME\n")
	writeFile(t, dir, "clean.ts", "const x = 1;\n")
	res := runDebtScan(t, dir)
	if res.Markers.TODO != 3 {
		t.Errorf("todo = %d, want 3 (three matching lines)", res.Markers.TODO)
	}
	if res.Markers.FIXME != 1 {
		t.Errorf("fixme = %d, want 1", res.Markers.FIXME)
	}
	if res.Files != 2 {
		t.Errorf("files = %d, want 2 (a.ts + b.ts; clean.ts has no markers)", res.Files)
	}
}

// Empty workdir defaults to CWD (no error). Mirrors RunSecretsScan contract.
func TestRunDebtScan_EmptyWorkdirFallsToCWD(t *testing.T) {
	_, err := RunDebtScan(context.Background(), DebtOptions{Workdir: ""})
	if err != nil {
		t.Fatalf("RunDebtScan with empty workdir should fall back to CWD, got error: %v", err)
	}
}

// Direct scanner interface — verifies line-counting semantics without
// touching the filesystem. Two TODO occurrences on a single line must count
// as one, mirroring grep -cE behavior.
func TestScanFileForDebt_LineCountSemantics(t *testing.T) {
	var m Markers
	hits := scanFileForDebt(strings.NewReader("// TODO TODO same line\n"), &m)
	if m.TODO != 1 {
		t.Errorf("todo = %d, want 1 (line-count, not occurrence-count)", m.TODO)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}
