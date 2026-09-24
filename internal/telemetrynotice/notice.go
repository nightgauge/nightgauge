// Package telemetrynotice prints the one-time disclosure that pairs with
// telemetry being on by default (Issue #738).
//
// The VSCode extension discloses through a modal. The CLI and the Go scheduler
// have no such surface, so without this package a CLI-only operator would be
// switched on by a release note they never read. Opt-out telemetry is a normal
// product default, but the thing that makes it defensible is that the operator
// is *told* — a default nobody is informed of is not opt-out, it is just
// undisclosed collection.
//
// The notice is deliberately cheap to ignore and impossible to miss twice: it
// goes to stderr (never stdout, which carries machine-readable output that a
// stray paragraph would corrupt), and a marker file in the machine-state root
// (layout.StateHome, ADR-024 § 8) keeps it to a single appearance per machine.
package telemetrynotice

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// markerName is the sentinel recording that this machine has seen the notice.
// The version suffix is load-bearing: a future material change to what is
// collected needs a new marker so the notice is shown again, rather than being
// suppressed by a file that attests to a different disclosure.
const markerName = "telemetry-notice-v1"

// Text is the disclosure itself. It states what is happening now, not what
// might happen, and it leads with the off switch — an operator scanning this
// in a wall of build output should be able to act on it without reading twice.
const Text = `
┌─ Nightgauge telemetry ─────────────────────────────────────────────────────┐
  Anonymous usage data is being sent to help improve Nightgauge. No source
  code, file contents, secrets, branch names, or commit SHAs are ever
  collected.

  To turn it off, add this to .nightgauge/config.yaml:

      platform:
        telemetry:
          enabled: false

  What is collected, in full: docs/TELEMETRY_PRIVACY.md
└────────────────────────────────────────────────────────────────────────────┘
`

// Notifier prints the disclosure at most once per state root.
type Notifier struct{ markerPath string }

// New builds a Notifier whose marker lives in stateRoot. Tests pass a temp
// directory; production uses ForAccount.
func New(stateRoot string) *Notifier {
	return &Notifier{markerPath: filepath.Join(stateRoot, markerName)}
}

// ForAccount builds a Notifier in the machine-state root. A marker left in
// the pre-ADR-024 ~/.nightgauge by an earlier release is moved there first, so
// an upgrade does not show the notice a second time. A move that cannot
// complete leaves the notice to be shown again at worst, which is not worth
// failing a command over, so only an unusable state root is an error.
func ForAccount() (*Notifier, error) {
	path, err := layout.StateFile(markerName)
	if path == "" {
		return nil, fmt.Errorf("resolve the telemetry-notice marker: %w", err)
	}
	return &Notifier{markerPath: path}, nil
}

// alreadyShown reports whether the marker exists. An unreadable marker counts
// as shown: re-printing on every invocation because a stat failed would be a
// worse failure than staying quiet, and the operator has by then seen it once.
func (n *Notifier) alreadyShown() bool {
	_, err := os.Stat(n.markerPath)
	return err == nil
}

// MaybePrint writes the disclosure to w and records that it has been shown,
// returning whether it printed.
//
// It prints only when telemetry is on *and* the operator has never said so
// themselves. Someone who wrote `enabled: true` has already been told — by
// their own hand — and does not need informing; someone who wrote
// `enabled: false` has nothing to be informed about. The notice exists for the
// one population the default actually moved.
//
// Failure to record the marker is not an error worth failing a command over:
// the notice has already been delivered, and the only cost of a lost marker is
// showing it again. The write error is returned so a caller that wants to log
// it can, and can be safely discarded otherwise.
func (n *Notifier) MaybePrint(w io.Writer, enabled, explicitlySet bool) (bool, error) {
	if !enabled || explicitlySet || n.alreadyShown() {
		return false, nil
	}
	if _, err := io.WriteString(w, Text); err != nil {
		return false, fmt.Errorf("write telemetry notice: %w", err)
	}
	return true, n.recordShown()
}

func (n *Notifier) recordShown() error {
	if err := os.MkdirAll(filepath.Dir(n.markerPath), 0o700); err != nil {
		return fmt.Errorf("create marker directory: %w", err)
	}
	if err := os.WriteFile(n.markerPath, []byte(""), 0o644); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}
