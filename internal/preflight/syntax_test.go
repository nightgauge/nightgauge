package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file at dir/relPath (parent dirs created as needed).
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func runSyntax(t *testing.T, dir string) *SyntaxCheckResult {
	t.Helper()
	res, err := RunSyntaxCheck(context.Background(), SyntaxOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunSyntaxCheck: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

func TestRunSyntaxCheck_EmptyWorkdir(t *testing.T) {
	dir := t.TempDir()
	res := runSyntax(t, dir)
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0", res.FilesScanned)
	}
	if res.FilesInvalid != 0 {
		t.Errorf("FilesInvalid = %d, want 0", res.FilesInvalid)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %v, want empty", res.Findings)
	}
}

func TestRunSyntaxCheck_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.json", `{"a": 1, "b": [2, 3]}`)
	res := runSyntax(t, dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.FilesInvalid != 0 {
		t.Errorf("FilesInvalid = %d, want 0", res.FilesInvalid)
	}
}

func TestRunSyntaxCheck_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.json", "{\n  \"a\": 1,\n  bad\n}")
	res := runSyntax(t, dir)
	if res.FilesInvalid != 1 {
		t.Fatalf("FilesInvalid = %d, want 1", res.FilesInvalid)
	}
	f := res.Findings[0]
	if f.Format != FormatJSON {
		t.Errorf("Format = %q, want %q", f.Format, FormatJSON)
	}
	if f.Line < 1 {
		t.Errorf("Line = %d, want >= 1 (extracted from json.SyntaxError.Offset)", f.Line)
	}
	if f.File != "bad.json" {
		t.Errorf("File = %q, want %q", f.File, "bad.json")
	}
	if strings.Contains(f.Error, "\n") {
		t.Errorf("Error contains newline (must be single-line): %q", f.Error)
	}
}

func TestRunSyntaxCheck_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.yaml", "name: thing\nitems:\n  - a\n  - b\n")
	res := runSyntax(t, dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.FilesInvalid != 0 {
		t.Errorf("FilesInvalid = %d, want 0; findings: %v", res.FilesInvalid, res.Findings)
	}
}

func TestRunSyntaxCheck_ValidYAMLDotYml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.yml", "key: value\n")
	res := runSyntax(t, dir)
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (.yml extension)", res.FilesScanned)
	}
}

func TestRunSyntaxCheck_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	// Tab as indentation is invalid in yaml.v3.
	writeFile(t, dir, "bad.yaml", "name: thing\n\tnested: bad\n")
	res := runSyntax(t, dir)
	if res.FilesInvalid != 1 {
		t.Fatalf("FilesInvalid = %d, want 1", res.FilesInvalid)
	}
	f := res.Findings[0]
	if f.Format != FormatYAML {
		t.Errorf("Format = %q, want %q", f.Format, FormatYAML)
	}
	if f.File != "bad.yaml" {
		t.Errorf("File = %q, want %q", f.File, "bad.yaml")
	}
}

func TestRunSyntaxCheck_MultiDocYAMLValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "multi.yaml", "doc1: a\n---\ndoc2: b\n")
	res := runSyntax(t, dir)
	if res.FilesInvalid != 0 {
		t.Errorf("multi-doc valid YAML flagged invalid; findings: %v", res.Findings)
	}
}

func TestRunSyntaxCheck_MultiDocYAMLInvalidSecondDoc(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "multi.yaml", "doc1: a\n---\n\tbad: indent\n")
	res := runSyntax(t, dir)
	if res.FilesInvalid != 1 {
		t.Errorf("multi-doc invalid YAML not flagged; findings: %v", res.Findings)
	}
}

func TestRunSyntaxCheck_ExcludedDirsPruned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/bad.json", "{not json")
	writeFile(t, dir, ".git/bad.json", "{not json")
	writeFile(t, dir, "vendor/bad.yaml", "\tbad")
	writeFile(t, dir, "dist/bad.json", "{not json")
	res := runSyntax(t, dir)
	if res.FilesInvalid != 0 {
		t.Errorf("excluded dirs not pruned; findings: %v", res.Findings)
	}
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0 (all in excluded dirs)", res.FilesScanned)
	}
}

func TestRunSyntaxCheck_NonTargetExtensionsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src.go", "package x // not scanned")
	writeFile(t, dir, "doc.md", "# heading")
	writeFile(t, dir, "config.toml", "[section]")
	res := runSyntax(t, dir)
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0 (none of these extensions are scanned)", res.FilesScanned)
	}
}

func TestRunSyntaxCheck_OversizeFileSkippedWithWarning(t *testing.T) {
	dir := t.TempDir()
	// Build a file just over the 5 MiB cap.
	big := make([]byte, syntaxMaxFileBytes+1)
	for i := range big {
		big[i] = ' '
	}
	full := filepath.Join(dir, "big.json")
	if err := os.WriteFile(full, big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res := runSyntax(t, dir)
	if res.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0 (oversize skipped)", res.FilesScanned)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("expected oversize warning, got none")
	}
}

func TestRunSyntaxCheck_EmptyFileIsValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "empty.json", "")
	writeFile(t, dir, "empty.yaml", "")
	res := runSyntax(t, dir)
	if res.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", res.FilesScanned)
	}
	if res.FilesInvalid != 0 {
		t.Errorf("empty files flagged invalid; findings: %v", res.Findings)
	}
}

func TestRunSyntaxCheck_TotalsSumFindings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.json", `{"a":1}`)
	writeFile(t, dir, "bad1.json", `{`)
	writeFile(t, dir, "bad2.yaml", "\tbad")
	res := runSyntax(t, dir)
	if res.FilesScanned != 3 {
		t.Errorf("FilesScanned = %d, want 3", res.FilesScanned)
	}
	if res.FilesInvalid != 2 {
		t.Errorf("FilesInvalid = %d, want 2", res.FilesInvalid)
	}
	if len(res.Findings) != res.FilesInvalid {
		t.Errorf("len(Findings) = %d, want %d", len(res.Findings), res.FilesInvalid)
	}
}

func TestRunSyntaxCheck_EmptyWorkdirFallsToCWD(t *testing.T) {
	_, err := RunSyntaxCheck(context.Background(), SyntaxOptions{Workdir: ""})
	if err != nil {
		t.Fatalf("empty workdir should fall back to CWD, got: %v", err)
	}
}

func TestRunSyntaxCheck_InvalidWorkdirReturnsError(t *testing.T) {
	_, err := RunSyntaxCheck(context.Background(), SyntaxOptions{Workdir: "/path/that/does/not/exist/xxx-preflight"})
	if err == nil {
		t.Errorf("expected error for nonexistent workdir, got nil")
	}
}

func TestParseYAMLLine(t *testing.T) {
	cases := []struct {
		msg  string
		want int
	}{
		{"yaml: line 5: mapping values are not allowed", 5},
		{"yaml: line 12: did not find expected key", 12},
		{"some other error", 0},
		{"yaml: invalid indent", 0},
	}
	for _, c := range cases {
		got := parseYAMLLine(c.msg)
		if got != c.want {
			t.Errorf("parseYAMLLine(%q) = %d, want %d", c.msg, got, c.want)
		}
	}
}

func TestLineFromOffset(t *testing.T) {
	data := []byte("line1\nline2\nline3\n")
	cases := []struct {
		offset int64
		want   int
	}{
		{0, 1},
		{3, 1},  // within "line1"
		{6, 2},  // start of "line2"
		{12, 3}, // start of "line3"
		{100, 4},
	}
	for _, c := range cases {
		got := lineFromOffset(data, c.offset)
		if got != c.want {
			t.Errorf("lineFromOffset(%d) = %d, want %d", c.offset, got, c.want)
		}
	}
}
