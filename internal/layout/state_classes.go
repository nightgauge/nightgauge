package layout

import "path/filepath"

// The machine-state classes under STATE (ADR-024 § 2, the rows for #2031 and
// #2032). Each is a directory or file name directly under the machine-state
// root; the legacy location of each is the same name under $HOME/.nightgauge.
const (
	// StateUsage holds the account-wide Claude rate-limit readings
	// (usage/claude-rate-limits.json, ADR-018) the statusline verb writes and
	// the extension reads.
	StateUsage = "usage"
	// StateOpenCode holds OpenCode's machine state: the per-run roots
	// (opencode/runs), preserved failure evidence (opencode/evidence), the
	// last-dispatch record and the published endpoint slots (ADR-022).
	StateOpenCode = "opencode"
	// StateLogs is the machine log directory, pruned by internal/logretention
	// (ADR-024 § 11).
	StateLogs = "logs"
)

// StateDir returns <StateHome>/name, creating the machine-state root (mode
// 0700, ADR-024 § 17) but not name itself: the writer of each class creates
// its own directory, 0700, and refuses a symlink in its place.
func StateDir(name string) (string, error) {
	root, err := StateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// StateDirPath is StateDir without creating anything, for a reader or a
// report that must not create the root as a side effect.
func StateDirPath(name string) (string, error) {
	root, err := StateHomePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}
