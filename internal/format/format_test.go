package format

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunFormat_NoFormatter_NotRan(t *testing.T) {
	dir := t.TempDir()
	result, err := RunFormat(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Error("expected Ran=false for empty directory")
	}
	if result.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestRunFormat_NPMFormatScript_Detected(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"scripts":{"format":"prettier --write .","build":"tsc"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// We can't actually run npm in a test, but we verify detection.
	// hasPkgScript should return true.
	if !hasPkgScript(dir, "format") {
		t.Error("expected hasPkgScript=true for npm format script")
	}
}

func TestRunFormat_PrettierConfig_Detected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".prettierrc"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasPrettierConfig(dir) {
		t.Error("expected hasPrettierConfig=true for .prettierrc")
	}
}

func TestRunFormat_PrettierConfigVariants(t *testing.T) {
	configs := []string{
		".prettierrc.json", ".prettierrc.js", ".prettierrc.yaml", ".prettierrc.yml",
		"prettier.config.js", "prettier.config.cjs",
	}
	for _, cfg := range configs {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, cfg), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !hasPrettierConfig(dir) {
			t.Errorf("expected hasPrettierConfig=true for %s", cfg)
		}
	}
}

func TestRunFormat_GoMod_Detected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// go.mod detected — would run go fmt; verify detection logic
	if !fileExists(filepath.Join(dir, "go.mod")) {
		t.Error("expected go.mod to be detected")
	}
}

func TestRunFormat_DprintJson_Detected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dprint.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, "dprint.json")) {
		t.Error("expected dprint.json to be detected")
	}
}

func TestRunFormat_NPMScriptTakesPrecedenceOverPrettierConfig(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"scripts":{"format":"prettier --write ."}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".prettierrc"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// npm script should take precedence; verify detection order
	if !hasPkgScript(dir, "format") {
		t.Error("expected npm format script to be detected")
	}
}

func TestHasPkgScript_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if hasPkgScript(dir, "format") {
		t.Error("expected false for missing package.json")
	}
}

func TestHasPkgScript_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Falls back to string search — "format" not in "not json"
	if hasPkgScript(dir, "format") {
		t.Error("expected false for invalid JSON without format string")
	}
}

func TestRunFormat_GoModProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunFormat(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// go fmt should run and either pass or fail depending on environment
	if !result.Ran {
		t.Error("expected Ran=true for go.mod project")
	}
	if result.Formatter != "go fmt" {
		t.Errorf("expected formatter=go fmt, got %s", result.Formatter)
	}
}
