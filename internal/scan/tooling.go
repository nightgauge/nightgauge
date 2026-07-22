// Tooling-presence scan verb. Replicates the linter / formatter config-file
// probes from skills/nightgauge-health-check/SKILL.md Phase 3.2 (and the
// equivalent in refactor-rewrite Phase 2.2) — audit row B5. Pure stat probes
// against fixed paths at workdir root plus targeted reads of pyproject.toml
// for the [tool.ruff] / [tool.black] / [tool.ruff.format] branches. No
// directory walk.
package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// ToolingScanResult is the stable JSON output schema for
// `nightgauge scan tooling`. Schema version 1 — do not rename or remove
// keys after first merge. linters and formatters are pre-populated with all
// keys at false so consumer jq paths never resolve to null.
type ToolingScanResult struct {
	V                int             `json:"v"`                 // schema version, always 1
	Workdir          string          `json:"workdir"`           // absolute path that was probed
	Linters          map[string]bool `json:"linters"`           // 7 fixed keys; see linterKeys
	Formatters       map[string]bool `json:"formatters"`        // 4 fixed keys; see formatterKeys
	LinterPresent    bool            `json:"linter_present"`    // true if ANY linter detected
	FormatterPresent bool            `json:"formatter_present"` // true if ANY formatter detected
	Warnings         []string        `json:"warnings"`          // non-fatal scan warnings
}

// ToolingOptions controls a single tooling-scan run.
type ToolingOptions struct {
	// Workdir is the directory to probe. When empty, the caller's CWD is used.
	Workdir string
}

const (
	linterESLint     = "eslint"
	linterRuff       = "ruff"
	linterGolangci   = "golangci"
	linterClippy     = "clippy"
	linterFlake8     = "flake8"
	linterPylint     = "pylint"
	linterCheckstyle = "checkstyle"

	formatterPrettier     = "prettier"
	formatterEditorconfig = "editorconfig"
	formatterBlack        = "black"
	formatterRuffFormat   = "ruff_format"
)

// linterKeys is the canonical, stable order of linter keys. Must remain
// stable after first merge — adding a key requires bumping schema V.
var linterKeys = []string{
	linterESLint, linterRuff, linterGolangci,
	linterClippy, linterFlake8, linterPylint, linterCheckstyle,
}

// formatterKeys is the canonical, stable order of formatter keys.
var formatterKeys = []string{
	formatterPrettier, formatterEditorconfig,
	formatterBlack, formatterRuffFormat,
}

// linterConfigFiles maps each linter to the set of root-level filenames whose
// presence implies the linter is configured. Mirrors SKILL.md L565-L575.
var linterConfigFiles = map[string][]string{
	linterESLint: {
		".eslintrc", ".eslintrc.js", ".eslintrc.json",
		".eslintrc.yml", "eslint.config.js", "eslint.config.mjs",
	},
	linterRuff:       {"ruff.toml"},
	linterGolangci:   {".golangci.yml", ".golangci.yaml"},
	linterClippy:     {"clippy.toml"},
	linterFlake8:     {".flake8"},
	linterPylint:     {".pylintrc"},
	linterCheckstyle: {"checkstyle.xml"},
}

// formatterConfigFiles maps each formatter to the set of root-level filenames
// whose presence implies the formatter is configured. Mirrors SKILL.md
// L578-L582.
var formatterConfigFiles = map[string][]string{
	formatterPrettier: {
		".prettierrc", ".prettierrc.js", ".prettierrc.json",
		".prettierrc.yml", "prettier.config.js",
	},
	formatterEditorconfig: {".editorconfig"},
}

// pyprojectMaxBytes caps the pyproject.toml read to avoid pathological inputs.
// Real pyproject files are KB-sized; 1 MiB is a safe upper bound.
const pyprojectMaxBytes int64 = 1 * 1024 * 1024

// pyproject regexes mirror SKILL.md L574 / L582 exactly. Anchored at line
// start with the `(?m)` multiline flag so they match TOML section headers.
var (
	pyprojectRuffRE       = regexp.MustCompile(`(?m)^\[tool\.ruff\]`)
	pyprojectBlackRE      = regexp.MustCompile(`(?m)^\[tool\.black\]`)
	pyprojectRuffFormatRE = regexp.MustCompile(`(?m)^\[tool\.ruff\.format\]`)
)

// RunToolingScan probes for linter and formatter configs at the workdir root
// and returns the structured result. Stat probes are O(linterCount +
// formatterCount); pyproject.toml is read once if present. Non-fatal: any
// read errors land in Warnings.
func RunToolingScan(ctx context.Context, opts ToolingOptions) (*ToolingScanResult, error) {
	workdir := opts.Workdir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workdir: %w", err)
		}
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve workdir: %w", err)
	}
	workdir = abs

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	result := &ToolingScanResult{
		V:          1,
		Workdir:    workdir,
		Linters:    make(map[string]bool, len(linterKeys)),
		Formatters: make(map[string]bool, len(formatterKeys)),
		Warnings:   []string{},
	}
	for _, k := range linterKeys {
		result.Linters[k] = false
	}
	for _, k := range formatterKeys {
		result.Formatters[k] = false
	}

	for tool, names := range linterConfigFiles {
		if anyExists(workdir, names) {
			result.Linters[tool] = true
		}
	}
	for tool, names := range formatterConfigFiles {
		if anyExists(workdir, names) {
			result.Formatters[tool] = true
		}
	}

	pyprojectPath := filepath.Join(workdir, "pyproject.toml")
	if info, statErr := os.Stat(pyprojectPath); statErr == nil && !info.IsDir() {
		if info.Size() > pyprojectMaxBytes {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("skip oversize pyproject.toml (%d bytes > %d)", info.Size(), pyprojectMaxBytes))
		} else {
			content, readErr := os.ReadFile(pyprojectPath)
			if readErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("read pyproject.toml: %v", readErr))
			} else {
				if pyprojectRuffRE.Match(content) {
					result.Linters[linterRuff] = true
				}
				if pyprojectBlackRE.Match(content) {
					result.Formatters[formatterBlack] = true
				}
				if pyprojectRuffFormatRE.Match(content) {
					result.Formatters[formatterRuffFormat] = true
				}
			}
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("stat pyproject.toml: %v", statErr))
	}

	for _, k := range linterKeys {
		if result.Linters[k] {
			result.LinterPresent = true
			break
		}
	}
	for _, k := range formatterKeys {
		if result.Formatters[k] {
			result.FormatterPresent = true
			break
		}
	}

	return result, nil
}

// anyExists returns true if any of names exists as a regular file (not a
// directory) at workdir root.
func anyExists(workdir string, names []string) bool {
	for _, n := range names {
		info, err := os.Stat(filepath.Join(workdir, n))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
