// Package acparse — Parse counts top-level Markdown acceptance-criteria
// checkboxes in an issue body and returns a deterministic verdict suitable
// for gating the feature-validate Phase 0.6 type:docs completion check.
//
// The function is the canonical replacement for the inline shell parser
// previously used in skills/nightgauge-feature-validate/SKILL.md
// Phase 0.6.2 (`gh issue view ... | grep -c '\- \[x\]'`). The shell version
// counted any `- [x]` / `- [ ]` substring anywhere in the body — including
// inside fenced code blocks, technical_notes examples, and prose. This
// package enforces start-of-line anchoring and skips fenced code blocks,
// removing the false positives from technical_notes YAML examples without
// changing the verdict on conforming issue bodies.
//
// Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B14.
package acparse

import (
	"bufio"
	"regexp"
	"strings"
)

// Result is the checkbox-tally verdict.
//
// Status is a closed string enum mirroring the existing Phase 0.6.3 gate:
//   - "passed"         — all top-level checkboxes are checked
//   - "failed"         — at least one top-level checkbox is unchecked
//   - "not_applicable" — the body contains no top-level checkboxes
//
// V locks the JSON shape at v1; bump on any breaking change to field names
// or semantics. The convention matches docs check-links (B6).
type Result struct {
	V         int    `json:"v"`
	Status    string `json:"status"`
	Checked   int    `json:"checked_count"`
	Unchecked int    `json:"unchecked_count"`
	Total     int    `json:"total"`
}

// Status values.
const (
	StatusPassed        = "passed"
	StatusFailed        = "failed"
	StatusNotApplicable = "not_applicable"
)

// checkboxRe matches a Markdown task-list item anchored to the start of a
// line (with optional leading whitespace for nested items). Bullets `-`,
// `*`, and `+` are all accepted, matching the Markdown task-list extension
// supported by GitHub. The capture group is the box state: empty (`" "`),
// lowercase x, or uppercase X.
var checkboxRe = regexp.MustCompile(`^[ \t]*[-*+][ \t]+\[([ xX])\][ \t]`)

// fenceRe identifies code-fence open/close lines (``` or ~~~ optionally
// followed by an info string). The toggle approach matches
// internal/docs/checklinks.go:88, keeping fence handling consistent across
// the deterministic Markdown stack.
var fenceRe = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})")

// Parse scans body line-by-line for top-level checkbox items and returns
// the deterministic verdict. The function is pure: identical inputs always
// produce identical outputs. CR-LF line endings are handled by bufio's
// scanner. Lines inside fenced code blocks are skipped.
func Parse(body string) Result {
	r := Result{V: 1}
	if body == "" {
		r.Status = StatusNotApplicable
		return r
	}

	scanner := bufio.NewScanner(strings.NewReader(body))
	// Issue bodies can include long lines (embedded JSON examples,
	// link blocks). Bump the buffer ceiling to 1 MiB to match the
	// approach in internal/docs/checklinks.go.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	inFence := false
	for scanner.Scan() {
		line := scanner.Text()

		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		m := checkboxRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "x", "X":
			r.Checked++
		case " ":
			r.Unchecked++
		}
	}

	r.Total = r.Checked + r.Unchecked
	switch {
	case r.Total == 0:
		r.Status = StatusNotApplicable
	case r.Unchecked == 0:
		r.Status = StatusPassed
	default:
		r.Status = StatusFailed
	}
	return r
}
