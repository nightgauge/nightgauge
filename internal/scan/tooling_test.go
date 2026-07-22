package scan

import (
	"context"
	"testing"
)

func runToolingScan(t *testing.T, dir string) *ToolingScanResult {
	t.Helper()
	res, err := RunToolingScan(context.Background(), ToolingOptions{Workdir: dir})
	if err != nil {
		t.Fatalf("RunToolingScan: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	return res
}

// Schema contract: every linter and formatter key is always present in the
// map (even if false), so consumer jq paths never resolve to null.
func TestRunToolingScan_AllKeysAlwaysPopulated(t *testing.T) {
	dir := t.TempDir()
	res := runToolingScan(t, dir)
	for _, k := range linterKeys {
		if _, ok := res.Linters[k]; !ok {
			t.Errorf("linters missing key %q", k)
		}
	}
	for _, k := range formatterKeys {
		if _, ok := res.Formatters[k]; !ok {
			t.Errorf("formatters missing key %q", k)
		}
	}
	if res.LinterPresent || res.FormatterPresent {
		t.Errorf("present flags should be false on empty workdir: %+v", res)
	}
}

// Each linter detected by its canonical config file at workdir root.
func TestRunToolingScan_LintersDetected(t *testing.T) {
	cases := []struct {
		filename string
		key      string
	}{
		{".eslintrc.json", linterESLint},
		{"eslint.config.mjs", linterESLint},
		{"ruff.toml", linterRuff},
		{".golangci.yml", linterGolangci},
		{"clippy.toml", linterClippy},
		{".flake8", linterFlake8},
		{".pylintrc", linterPylint},
		{"checkstyle.xml", linterCheckstyle},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tc.filename, "")
			res := runToolingScan(t, dir)
			if !res.Linters[tc.key] {
				t.Errorf("linters[%q] = false after creating %q", tc.key, tc.filename)
			}
			if !res.LinterPresent {
				t.Errorf("linter_present = false after creating %q", tc.filename)
			}
		})
	}
}

// Each formatter detected by its canonical config file.
func TestRunToolingScan_FormattersDetected(t *testing.T) {
	cases := []struct {
		filename string
		key      string
	}{
		{".prettierrc", formatterPrettier},
		{"prettier.config.js", formatterPrettier},
		{".editorconfig", formatterEditorconfig},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tc.filename, "")
			res := runToolingScan(t, dir)
			if !res.Formatters[tc.key] {
				t.Errorf("formatters[%q] = false after creating %q", tc.key, tc.filename)
			}
			if !res.FormatterPresent {
				t.Errorf("formatter_present = false after creating %q", tc.filename)
			}
		})
	}
}

// pyproject.toml branches: [tool.ruff] → linters.ruff,
// [tool.black] → formatters.black, [tool.ruff.format] → formatters.ruff_format.
func TestRunToolingScan_PyprojectRuffBranch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.ruff]\nline-length = 100\n")
	res := runToolingScan(t, dir)
	if !res.Linters[linterRuff] {
		t.Errorf("linters.ruff = false after pyproject.toml [tool.ruff]")
	}
	if !res.LinterPresent {
		t.Errorf("linter_present = false after pyproject.toml [tool.ruff]")
	}
}

func TestRunToolingScan_PyprojectBlackBranch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.black]\nline-length = 88\n")
	res := runToolingScan(t, dir)
	if !res.Formatters[formatterBlack] {
		t.Errorf("formatters.black = false after pyproject.toml [tool.black]")
	}
	if !res.FormatterPresent {
		t.Errorf("formatter_present = false after pyproject.toml [tool.black]")
	}
}

func TestRunToolingScan_PyprojectRuffFormatBranch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[tool.ruff.format]\nquote-style = \"double\"\n")
	res := runToolingScan(t, dir)
	if !res.Formatters[formatterRuffFormat] {
		t.Errorf("formatters.ruff_format = false after pyproject.toml [tool.ruff.format]")
	}
}

// pyproject.toml without any [tool.*] match must not flip any flag.
func TestRunToolingScan_PyprojectNoMatchingSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	res := runToolingScan(t, dir)
	if res.LinterPresent || res.FormatterPresent {
		t.Errorf("expected no linters/formatters detected, got %+v", res)
	}
}

// Mixed config: tooling spread across multiple files. Verifies multiple
// detections in one run.
func TestRunToolingScan_MixedConfigs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".eslintrc.json", "{}")
	writeFile(t, dir, ".prettierrc", "{}")
	writeFile(t, dir, ".golangci.yml", "")
	writeFile(t, dir, "pyproject.toml", "[tool.ruff]\n[tool.black]\n")
	res := runToolingScan(t, dir)
	if !res.Linters[linterESLint] || !res.Linters[linterGolangci] || !res.Linters[linterRuff] {
		t.Errorf("expected eslint+golangci+ruff linters, got %+v", res.Linters)
	}
	if !res.Formatters[formatterPrettier] || !res.Formatters[formatterBlack] {
		t.Errorf("expected prettier+black formatters, got %+v", res.Formatters)
	}
}

// Empty workdir defaults to CWD (no error).
func TestRunToolingScan_EmptyWorkdirFallsToCWD(t *testing.T) {
	_, err := RunToolingScan(context.Background(), ToolingOptions{Workdir: ""})
	if err != nil {
		t.Fatalf("RunToolingScan with empty workdir should fall back to CWD, got error: %v", err)
	}
}

// A directory at the linter-config path (e.g., someone created a `.flake8/`
// directory) must not be treated as a config file.
func TestRunToolingScan_DirectoryNotTreatedAsConfigFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".flake8/inner.txt", "") // creates .flake8/ as a dir
	res := runToolingScan(t, dir)
	if res.Linters[linterFlake8] {
		t.Errorf("linters.flake8 = true when .flake8 is a directory, want false")
	}
}
