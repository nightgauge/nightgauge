package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// Recorded pauses for scheduled automations (#2090). Pausing is the operator's
// explicit decision that an automation is stopped on purpose. It is recorded,
// never inferred, and it does not suppress the finding: a paused automation
// that has stopped is still reported, as info, until the operator resumes it.

// AutomationPause is one recorded decision.
type AutomationPause struct {
	ID       string    `json:"id"`
	PausedAt time.Time `json:"paused_at"`
	Reason   string    `json:"reason,omitempty"`
}

type automationPauseFile struct {
	Pauses []AutomationPause `json:"pauses"`
}

// AutomationPausePath is the per-checkout pause record for workspaceRoot:
// CHECKOUT/doctor/automation-pauses.json (ADR-024 § 7). A root outside git has
// none and returns the resolver's error.
func AutomationPausePath(workspaceRoot string) (string, error) {
	return layout.CheckoutPath(workspaceRoot, layout.CheckoutAutomationPauses)
}

// LoadAutomationPauses reads the recorded pauses, keyed by automation ID. A
// missing file, or a root outside git (no per-checkout directory), is no
// pauses.
func LoadAutomationPauses(workspaceRoot string) (map[string]AutomationPause, error) {
	out := map[string]AutomationPause{}
	path, err := AutomationPausePath(workspaceRoot)
	if errors.Is(err, layout.ErrNotGitRepository) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return out, fmt.Errorf("automation pause record %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var f automationPauseFile
	if err := json.Unmarshal(data, &f); err != nil {
		return out, fmt.Errorf("automation pause record %s: %w", path, err)
	}
	for _, p := range f.Pauses {
		out[p.ID] = p
	}
	return out, nil
}

// PauseAutomation records that automation id is intentionally paused.
func PauseAutomation(workspaceRoot, id, reason string, now time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("automation id is required")
	}
	pauses, err := LoadAutomationPauses(workspaceRoot)
	if err != nil {
		return err
	}
	pauses[id] = AutomationPause{ID: id, PausedAt: now.UTC(), Reason: strings.TrimSpace(reason)}
	return writeAutomationPauses(workspaceRoot, pauses)
}

// ResumeAutomation removes a recorded pause. It reports whether one existed.
func ResumeAutomation(workspaceRoot, id string) (bool, error) {
	pauses, err := LoadAutomationPauses(workspaceRoot)
	if err != nil {
		return false, err
	}
	if _, ok := pauses[id]; !ok {
		return false, nil
	}
	delete(pauses, id)
	return true, writeAutomationPauses(workspaceRoot, pauses)
}

// writeAutomationPauses writes the record atomically: directory 0700, file
// 0600, and never through a symlinked directory (ADR-025 § 8).
func writeAutomationPauses(workspaceRoot string, pauses map[string]AutomationPause) error {
	path, err := AutomationPausePath(workspaceRoot)
	if err != nil {
		return err
	}
	// CheckoutSubdir creates the directory 0700 and refuses a symlink.
	dir, err := layout.CheckoutSubdir(workspaceRoot, filepath.Dir(layout.CheckoutAutomationPauses))
	if err != nil {
		return err
	}
	f := automationPauseFile{Pauses: make([]AutomationPause, 0, len(pauses))}
	for _, p := range pauses {
		f.Pauses = append(f.Pauses, p)
	}
	sort.Slice(f.Pauses, func(i, j int) bool { return f.Pauses[i].ID < f.Pauses[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".automation-pauses-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
