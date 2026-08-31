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

// Item is one top-level acceptance-criteria checkbox, in document order.
//
// Index is 1-based and counts the same items Parse counts, so a caller can
// name a criterion by the position Parse already reported. Text is the
// criterion with its bullet and box stripped, which is what a reviewer (or a
// stage evaluating criteria against a diff) actually needs to reason about.
type Item struct {
	Index   int    `json:"index"`
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
	// Line is the 0-based index into the body's lines, retained so Mark can
	// rewrite in place without re-deriving the scan.
	Line int `json:"-"`
}

// MarkResult records what Mark did, per index.
//
// The three outcomes are distinct on purpose. "Already checked" is a success
// and must not be reported as a change (Mark is idempotent); "not found" is a
// caller error — an index nobody can satisfy — and must never be silently
// folded into success, which is how a gate ends up believing a criterion it
// never marked.
type MarkResult struct {
	V              int   `json:"v"`
	Changed        []int `json:"changed"`
	AlreadyChecked []int `json:"already_checked"`
	NotFound       []int `json:"not_found"`
}

// splitLines splits body into lines, preserving each line's own terminator so
// a rewrite is byte-identical everywhere it did not deliberately edit.
// bufio.Scanner cannot be used here: it discards the terminator, which would
// silently normalise CRLF bodies to LF and rewrite lines nobody asked to touch.
func splitLines(body string) []string {
	if body == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			out = append(out, body[start:i+1])
			start = i + 1
		}
	}
	if start < len(body) {
		out = append(out, body[start:])
	}
	return out
}

// List returns every top-level checkbox in document order, applying the same
// fence-skipping and start-of-line anchoring as Parse. Sharing the scan is the
// point: an index List reports and an index Mark writes must mean the same
// criterion, and the only way to guarantee that is one traversal rule.
func List(body string) []Item {
	lines := splitLines(body)
	var items []Item
	inFence := false
	idx := 0
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := checkboxRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		state := line[m[2]:m[3]]
		idx++
		items = append(items, Item{
			Index:   idx,
			Text:    strings.TrimSpace(line[m[1]:]),
			Checked: state == "x" || state == "X",
			Line:    i,
		})
	}
	return items
}

// Mark ticks the checkboxes at the given 1-based indices and returns the
// rewritten body.
//
// Only the box character changes. The bullet, indentation, criterion text and
// line terminator are preserved byte-for-byte, and lines carrying no requested
// index are untouched — an issue body is a human artefact and a verb that
// reformats it while ticking a box is not one anybody will trust with write
// access.
//
// Idempotent: an index already checked is reported under AlreadyChecked and the
// body is returned unchanged for it. Unknown indices are reported under
// NotFound and change nothing, so a caller that miscounts fails loudly instead
// of marking the wrong criterion.
func Mark(body string, indices []int) (string, MarkResult) {
	res := MarkResult{V: 1}
	items := List(body)
	byIndex := make(map[int]Item, len(items))
	for _, it := range items {
		byIndex[it.Index] = it
	}

	lines := splitLines(body)
	seen := make(map[int]bool, len(indices))
	for _, want := range indices {
		if seen[want] {
			continue // a repeated index is one request, not two
		}
		seen[want] = true

		it, ok := byIndex[want]
		if !ok {
			res.NotFound = append(res.NotFound, want)
			continue
		}
		if it.Checked {
			res.AlreadyChecked = append(res.AlreadyChecked, want)
			continue
		}

		raw := lines[it.Line]
		trimmed := strings.TrimRight(raw, "\r\n")
		term := raw[len(trimmed):]
		m := checkboxRe.FindStringSubmatchIndex(trimmed)
		if m == nil {
			// The scan that produced this item used the same regexp, so this
			// is unreachable; treat it as not-found rather than guessing at an
			// offset and corrupting the line.
			res.NotFound = append(res.NotFound, want)
			continue
		}
		lines[it.Line] = trimmed[:m[2]] + "x" + trimmed[m[3]:] + term
		res.Changed = append(res.Changed, want)
	}

	return strings.Join(lines, ""), res
}
