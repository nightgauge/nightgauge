package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The Finding and Remedy types are the contract ADR-025 fixes for every doctor
// surface: the CLI, `--json`, IPC, the VS Code panel and Action Center cards.
// Field names and JSON tags must match the ADR; a field the ADR lacks is added
// by amending the ADR first.

// Severity ranks a finding. Exactly four values exist (ADR-025 § 2).
type Severity string

const (
	// SeverityBlocker: the pipeline cannot run. Exit code 2.
	SeverityBlocker Severity = "blocker"
	// SeverityWarning: the pipeline runs degraded. Exit code 1.
	SeverityWarning Severity = "warning"
	// SeverityHousekeeping: cleanup; never degrades status or the exit code.
	SeverityHousekeeping Severity = "housekeeping"
	// SeverityInfo: context, including skipped dependencies. Never changes the exit code.
	SeverityInfo Severity = "info"
)

// Severities lists every severity, most severe first. Renderers group by it.
var Severities = []Severity{SeverityBlocker, SeverityWarning, SeverityHousekeeping, SeverityInfo}

// rank orders severities for sorting; lower is more severe.
func (s Severity) rank() int {
	for i, v := range Severities {
		if v == s {
			return i
		}
	}
	return len(Severities)
}

// RemedyKind says how a remedy may be applied (ADR-025 § 3).
type RemedyKind string

const (
	// RemedyAuto is safe and reversible or idempotent; `--fix` applies it without a prompt.
	RemedyAuto RemedyKind = "auto"
	// RemedyConfirm is destructive or visible to others and needs explicit consent.
	RemedyConfirm RemedyKind = "confirm"
	// RemedyManual can only be carried out by the operator.
	RemedyManual RemedyKind = "manual"
)

// Finding is one coded condition a check detected. A check that returns no
// findings passed.
type Finding struct {
	Code        string            `json:"code"`        // "NGD014"; stable, never reused
	Check       string            `json:"check"`       // registry ID, e.g. "worktree_leaks"
	Severity    Severity          `json:"severity"`    // blocker|warning|housekeeping|info
	Title       string            `json:"title"`       // one line, no trailing period
	Cause       string            `json:"cause"`       // plain-language why
	Evidence    map[string]string `json:"evidence"`    // structured, redacted before render
	Docs        string            `json:"docs"`        // "docs/DOCTOR.md#ngd014"
	Fingerprint string            `json:"fingerprint"` // stable across runs
	Remedies    []Remedy          `json:"remedies"`    // may be empty; first is preferred
}

// Remedy is one registered way to resolve a finding. Verb names an entry in
// the closed Go verb registry; it is never a shell string.
type Remedy struct {
	ID         string     `json:"id"`              // unique within the finding, e.g. "remove"
	Kind       RemedyKind `json:"kind"`            // auto|confirm|manual
	Summary    string     `json:"summary"`         // one line, imperative
	Preview    string     `json:"preview"`         // exactly what --dry-run prints
	Verb       string     `json:"verb"`            // registered Go verb; empty for manual
	Reversible bool       `json:"reversible"`      // whether apply can be undone
	Verify     string     `json:"verify"`          // check ID re-run after apply
	Steps      []string   `json:"steps,omitempty"` // manual only
	Links      []string   `json:"links,omitempty"` // manual only; allowlisted hosts
}

// Summary counts findings per severity (JSON v2 `summary`).
type Summary struct {
	Blocker      int `json:"blocker"`
	Warning      int `json:"warning"`
	Housekeeping int `json:"housekeeping"`
	Info         int `json:"info"`
}

// codeTimeout is reserved for "check could not complete" (timeout or internal
// error). It is always a warning and applies to any check.
const codeTimeout = "NGD000"

// DocsAnchor is the docs/DOCTOR.md anchor for a code.
func DocsAnchor(code string) string {
	return "docs/DOCTOR.md#" + strings.ToLower(code)
}

// Fingerprint is sha256(code + "\x00" + identity keys joined by "\x00"),
// truncated to 16 hex characters. Identity keys name the object (a path, a
// branch, a repository) and never carry volatile values such as counts.
func Fingerprint(code string, identity ...string) string {
	sum := sha256.Sum256([]byte(code + "\x00" + strings.Join(identity, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// Summarize counts findings by severity.
func Summarize(findings []Finding) Summary {
	var s Summary
	for _, f := range findings {
		switch f.Severity {
		case SeverityBlocker:
			s.Blocker++
		case SeverityWarning:
			s.Warning++
		case SeverityHousekeeping:
			s.Housekeeping++
		case SeverityInfo:
			s.Info++
		}
	}
	return s
}
