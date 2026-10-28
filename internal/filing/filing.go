// Package filing holds the controls every autonomous issue-filer shares
// (#480, docs/SELF_IMPROVEMENT_BOUNDARIES.md Rule 5): the closed type,
// priority and size vocabularies, the follow_up_filing gate that fails closed
// to report-only, the loud per-run cap, the board-field mapping, and the
// provenance and untrusted-text rendering used in filed issue bodies.
//
// Both filers use it: spike-materialize (internal/cmd/spike) and
// followup-materialize (internal/cmd/followup). Nothing here talks to a forge.
package filing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Closed vocabularies. A value outside these never reaches a forge call.
var (
	AllowedTypes      = map[string]bool{"feature": true, "bug": true, "docs": true, "chore": true, "spike": true}
	AllowedPriorities = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	AllowedSizes      = map[string]bool{"XS": true, "S": true, "M": true, "L": true, "XL": true}
)

// ValidateFields checks type, priority and size against the closed
// vocabularies. Errors name the field first.
func ValidateFields(typ, priority, size string) error {
	if !AllowedTypes[typ] {
		return fmt.Errorf("type: must be one of feature|bug|docs|chore|spike (got %q)", typ)
	}
	if !AllowedPriorities[priority] {
		return fmt.Errorf("priority: must be one of critical|high|medium|low (got %q)", priority)
	}
	if !AllowedSizes[size] {
		return fmt.Errorf("size: must be one of XS|S|M|L|XL (got %q)", size)
	}
	return nil
}

// Mode is what a filer may do this run.
type Mode string

const (
	// ModeFile creates issues.
	ModeFile Mode = "file"
	// ModeReport files nothing and emits the would-file list.
	ModeReport Mode = "report"
)

// Built-in defaults for follow_up_filing. The customer default is report-only.
const (
	DefaultMode = ModeReport
	DefaultCap  = 10
)

// Gate is the resolved follow_up_filing configuration.
type Gate struct {
	Mode Mode `json:"mode"`
	// Reason says why Mode is report when a switch or a fault forced it.
	Reason         string `json:"reason,omitempty"`
	Cap            int    `json:"max_per_run"`
	AllowUntrusted bool   `json:"allow_untrusted_authors"`
}

// Files reports whether the gate permits creating issues.
func (g Gate) Files() bool { return g.Mode == ModeFile }

// ConfigPath is where ResolveGate reads the gate from.
func ConfigPath(workdir string) string {
	return filepath.Join(workdir, ".nightgauge", "config.yaml")
}

// ResolveGate reads follow_up_filing from workdir's .nightgauge/config.yaml.
// It FAILS CLOSED: a missing, unreadable or unparseable config resolves to
// report-only with a reason, never to a filing run. An absent
// follow_up_filing key inside a valid config takes the built-in defaults.
func ResolveGate(workdir string) Gate {
	data, err := os.ReadFile(ConfigPath(workdir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return closed("config missing: " + ConfigPath(workdir))
		}
		return closed("config unreadable: " + err.Error())
	}
	return ParseGate(data)
}

// ParseGate resolves the gate from config bytes, failing closed on a parse
// error or an invalid value.
func ParseGate(data []byte) Gate {
	var doc struct {
		FollowUpFiling *struct {
			Enabled               *bool   `yaml:"enabled"`
			Mode                  *string `yaml:"mode"`
			MaxPerRun             *int    `yaml:"max_per_run"`
			AllowUntrustedAuthors *bool   `yaml:"allow_untrusted_authors"`
		} `yaml:"follow_up_filing"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return closed("config unparseable: " + err.Error())
	}
	g := Gate{Mode: DefaultMode, Cap: DefaultCap}
	f := doc.FollowUpFiling
	if f == nil {
		g.Reason = "follow_up_filing.mode defaults to report"
		return g
	}
	if f.MaxPerRun != nil {
		if *f.MaxPerRun < 0 {
			return closed(fmt.Sprintf("follow_up_filing.max_per_run: must be >= 0 (got %d)", *f.MaxPerRun))
		}
		g.Cap = *f.MaxPerRun
	}
	if f.AllowUntrustedAuthors != nil {
		g.AllowUntrusted = *f.AllowUntrustedAuthors
	}
	if f.Mode != nil {
		switch Mode(*f.Mode) {
		case ModeFile, ModeReport:
			g.Mode = Mode(*f.Mode)
		default:
			return closed(fmt.Sprintf("follow_up_filing.mode: must be file or report (got %q)", *f.Mode))
		}
	} else {
		g.Reason = "follow_up_filing.mode defaults to report"
	}
	if f.Enabled != nil && !*f.Enabled {
		g.Mode = ModeReport
		g.Reason = "follow_up_filing.enabled is false (kill switch)"
	}
	return g
}

func closed(reason string) Gate {
	return Gate{Mode: ModeReport, Reason: "fail closed: " + reason, Cap: DefaultCap}
}

// CapError is returned when a run had more to file than its cap allowed. The
// refused items are never silently dropped: the caller keeps them.
type CapError struct {
	Refused int
	Total   int
	Cap     int
}

func (e *CapError) Error() string {
	return fmt.Sprintf("per-run cap: refused: %d of %d (follow_up_filing.max_per_run=%d)", e.Refused, e.Total, e.Cap)
}

// ApplyCap splits n to-create items into the first accepted and the refused
// remainder. It returns a *CapError when anything was refused.
func ApplyCap(n, limit int) (accepted int, err error) {
	if n <= limit {
		return n, nil
	}
	return limit, &CapError{Refused: n - limit, Total: n, Cap: limit}
}

// PriorityToBoardOption maps the contract's lowercase priority to the project
// board's "P0..P3" option names.
func PriorityToBoardOption(priority string) string {
	switch priority {
	case "critical":
		return "P0"
	case "high":
		return "P1"
	case "medium":
		return "P2"
	case "low":
		return "P3"
	}
	return "P2"
}

// RenderUntrusted renders record-supplied text for an issue body: bounded to
// max runes and fenced so Markdown, mentions and HTML inside it stay inert.
// Backticks are neutralized so the text cannot close its own fence.
func RenderUntrusted(text string, max int) string {
	r := []rune(text)
	if max > 0 && len(r) > max {
		text = string(r[:max]) + "…"
	}
	text = strings.ReplaceAll(text, "`", "ˋ")
	return "```text\n" + text + "\n```"
}

// ProvenanceLine is the line every follow-up body carries.
func ProvenanceLine(issue int, runID, stage string) string {
	if strings.TrimSpace(runID) == "" {
		runID = "unknown"
	}
	return fmt.Sprintf("Discovered in #%d (run %s, stage %s)", issue, runID, stage)
}
