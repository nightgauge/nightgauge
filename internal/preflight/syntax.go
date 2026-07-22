// Package preflight provides deterministic pre-submission gate checks. The
// SyntaxCheckResult and SkillVersionsResult JSON schemas are stable — field
// names and types must not change after first merge. Skills parse
// `nightgauge preflight ... --json` output; any breaking change requires
// incrementing the V field.
//
// The syntax verb walks a workdir for *.json|*.yaml|*.yml files and validates
// each. It replaces the `python3 -m json.tool` and `python3 -c "import yaml"`
// chains in skills/pr-preflight/SKILL.md Checks 2 and 3 (audit row B40,
// skill-survey row 58).
package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SyntaxCheckResult is the stable JSON output schema for
// `nightgauge preflight syntax`. Schema version 1 — do not rename or
// remove fields after first merge.
type SyntaxCheckResult struct {
	V            int             `json:"v"`             // schema version, always 1
	Workdir      string          `json:"workdir"`       // absolute path that was scanned
	FilesScanned int             `json:"files_scanned"` // count of *.json|*.yaml|*.yml files inspected
	FilesInvalid int             `json:"files_invalid"` // count of findings (== len(Findings))
	Findings     []SyntaxFinding `json:"findings"`      // one entry per parse failure
	Warnings     []string        `json:"warnings"`      // non-fatal scan warnings (unreadable files, oversize-skips)
}

// SyntaxFinding records a single JSON or YAML parse failure.
type SyntaxFinding struct {
	File   string `json:"file"`   // path relative to workdir
	Line   int    `json:"line"`   // 1-based line number; 0 when unknown
	Format string `json:"format"` // closed enum: "json" | "yaml"
	Error  string `json:"error"`  // parse-error message, single line
}

// Format values emitted in SyntaxFinding.Format. Closed enum — adding a new
// format requires bumping the schema V field.
const (
	FormatJSON = "json"
	FormatYAML = "yaml"
)

// SyntaxOptions controls a single syntax-check run.
type SyntaxOptions struct {
	// Workdir is the directory to scan. When empty, the caller's CWD is used.
	Workdir string
}

// syntaxExcludedDirs are pruned at the WalkDir level. Mirrors the prune set
// from internal/scan/secrets.go so consumers see consistent walk semantics
// across preflight verbs.
var syntaxExcludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
	".next":        {},
	"out":          {},
}

// syntaxMaxFileBytes is the largest file the validator will inspect. Files
// larger than this are recorded as warnings and skipped (matches
// secretsMaxFileBytes — package-lock.json or generated bundles can be huge).
const syntaxMaxFileBytes int64 = 5 * 1024 * 1024

// RunSyntaxCheck walks the workdir and validates every *.json|*.yaml|*.yml
// file. Non-fatal by design — unreadable files and oversize skips are
// recorded in Warnings. err is reserved for hard input errors (invalid
// workdir).
func RunSyntaxCheck(ctx context.Context, opts SyntaxOptions) (*SyntaxCheckResult, error) {
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
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("workdir %q is not a readable directory", workdir)
	}
	workdir = abs

	result := &SyntaxCheckResult{
		V:        1,
		Workdir:  workdir,
		Findings: []SyntaxFinding{},
		Warnings: []string{},
	}

	walkErr := filepath.WalkDir(workdir, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("walk %s: %v", rel, err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == workdir {
				return nil
			}
			if _, skip := syntaxExcludedDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(d.Name()))
		var format string
		switch ext {
		case ".json":
			format = FormatJSON
		case ".yaml", ".yml":
			format = FormatYAML
		default:
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("stat %s: %v", rel, infoErr))
			return nil
		}
		if info.Size() > syntaxMaxFileBytes {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip oversize %s (%d bytes > %d)", rel, info.Size(), syntaxMaxFileBytes))
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			rel := relOrAbs(workdir, path)
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", rel, readErr))
			return nil
		}

		result.FilesScanned++

		if finding, ok := validateBytes(data, format); ok {
			finding.File = filepath.ToSlash(relOrAbs(workdir, path))
			result.Findings = append(result.Findings, finding)
		}
		return nil
	})
	if walkErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("walk aborted: %v", walkErr))
	}

	result.FilesInvalid = len(result.Findings)
	return result, nil
}

// validateBytes parses data as the given format. Returns (finding, true) when
// invalid; (zero, false) when parses cleanly. Empty files are considered
// valid for both formats (matches `python3 -m json.tool` and yaml.safe_load
// behavior on a zero-byte input — neither raises).
func validateBytes(data []byte, format string) (SyntaxFinding, bool) {
	if len(data) == 0 {
		return SyntaxFinding{}, false
	}
	switch format {
	case FormatJSON:
		var v interface{}
		if err := json.Unmarshal(data, &v); err != nil {
			line := 0
			var syntaxErr *json.SyntaxError
			if errors.As(err, &syntaxErr) {
				line = lineFromOffset(data, syntaxErr.Offset)
			}
			return SyntaxFinding{
				Line:   line,
				Format: FormatJSON,
				Error:  singleLine(err.Error()),
			}, true
		}
	case FormatYAML:
		// yaml.v3's Decoder supports multi-document streams natively. We
		// iterate until io.EOF so a stream with a valid first doc and an
		// invalid second doc still surfaces the error.
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var v interface{}
			err := dec.Decode(&v)
			if err == nil {
				continue
			}
			if errors.Is(err, io.EOF) {
				return SyntaxFinding{}, false
			}
			return SyntaxFinding{
				Line:   parseYAMLLine(err.Error()),
				Format: FormatYAML,
				Error:  singleLine(err.Error()),
			}, true
		}
	}
	return SyntaxFinding{}, false
}

// lineFromOffset converts a byte offset within data into a 1-based line
// number. json.SyntaxError.Offset points at the byte just past the failure;
// we count newlines up to and including that offset.
func lineFromOffset(data []byte, offset int64) int {
	if offset <= 0 {
		return 1
	}
	if int(offset) > len(data) {
		offset = int64(len(data))
	}
	line := 1
	for i := int64(0); i < offset; i++ {
		if data[i] == '\n' {
			line++
		}
	}
	return line
}

// parseYAMLLine extracts the line number from a yaml.v3 error message.
// yaml.v3 errors are formatted as "yaml: line N: <description>" or
// "yaml: line N: mapping values are not allowed in this context". When no
// line is present (mapping/scalar errors), returns 0.
func parseYAMLLine(msg string) int {
	const prefix = "line "
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len(prefix):]
	end := strings.IndexAny(rest, ":, ")
	if end <= 0 {
		return 0
	}
	n := 0
	for i := 0; i < end; i++ {
		c := rest[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// singleLine collapses any embedded newlines so the JSON Error field renders
// as a single string. yaml.v3 errors occasionally include a multi-line
// context block.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

// relOrAbs returns path relative to workdir for warning messages, falling
// back to the absolute path if the rel computation fails.
func relOrAbs(workdir, path string) string {
	if rel, err := filepath.Rel(workdir, path); err == nil {
		return rel
	}
	return path
}
