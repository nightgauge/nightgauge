package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// RemovalLedgerName is the append-only file, in a worktree's parent
// directory, where the pipeline records each worktree it removes (#2701).
//
// One line per removal: "<unix-epoch-seconds>\t<name>\n", where <name> is
// the worktree directory's base name. scripts/state-backstop.sh reads it to
// excuse a removal the pipeline made during a gate run; the extension's
// WorktreeManager.cleanup writes the same format. The file is never deleted
// or rewritten: removing it would itself be a removed machine-state path.
const RemovalLedgerName = ".removed-worktrees"

// removalNow is the ledger's clock, replaced in tests.
var removalNow = time.Now

// RecordWorktreeRemoval appends the ledger line for the worktree at path.
// Call it after deciding to remove the worktree and before removing it, so a
// crash mid-removal still leaves the evidence.
func RecordWorktreeRemoval(path string) error {
	clean := filepath.Clean(path)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == RemovalLedgerName {
		return fmt.Errorf("record worktree removal: invalid worktree path %q", path)
	}
	ledger := filepath.Join(filepath.Dir(clean), RemovalLedgerName)
	f, err := os.OpenFile(ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("record worktree removal: %w", err)
	}
	line := strconv.FormatInt(removalNow().Unix(), 10) + "\t" + name + "\n"
	// One write per line: O_APPEND keeps concurrent writers' lines whole.
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("record worktree removal: %w", err)
	}
	return f.Close()
}
