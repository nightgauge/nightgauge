package doctor

// The `ledger_daemon_coverage` arm (#1913).
//
// The API ledger is the only instrument that prices a GitHub call, and a
// daemon is the biggest single spender in a workspace — it sweeps boards for
// every repository on a timer. When the daemon's records go somewhere else,
// nothing breaks and nothing warns: `api-usage` reports a quiet workspace,
// and the first symptom is an unattributable exhaustion hours later.
//
// That is exactly what happened. `serve --workspace <root>` never chdirs, and
// the extension spawned it without a cwd, so the ledger resolved its relative
// path against the extension host's directory — not a workspace, so no file
// was opened at all. Three hours of sweeps across six repositories, zero
// records. The fix is elsewhere (github.SetAPILedgerWorkspaceRoot); this arm
// is what makes the same class of gap loud the next time it opens.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// ledgerCoverageWindow is how far back the arm looks for the daemon's own
// records. One heartbeat interval is the shortest window in which a live
// daemon is definitely doing something; anything shorter would fire on the
// gap between two sweeps.
var ledgerCoverageWindow = runstate.ServeHeartbeatInterval

// checkLedgerDaemonCoverage reports a live, active daemon whose GitHub traffic
// is missing from this workspace's ledger.
//
// It deliberately refuses to fire on heartbeat alone. A daemon with nothing to
// do makes no GitHub calls, and "no records" is then the correct, healthy
// answer — indistinguishable from a misrouted ledger on that evidence alone.
// So the finding requires independent proof the daemon has been working: a
// pipeline state file touched inside the same window. Narrowing the window
// instead would trade this false negative for a false positive on every idle
// period, and an arm that cries wolf during normal quiet is an arm operators
// switch off.
func checkLedgerDaemonCoverage(workspaceRoot string, now time.Time) (CheckItem, string) {
	if workspaceRoot == "" {
		return CheckItem{OK: true, Detail: "ledger coverage not checked (no workspace root)"}, ""
	}

	holder, held := runstate.InspectServeLease(workspaceRoot)
	if !held {
		return CheckItem{OK: true, Detail: "no daemon is serving this workspace"}, ""
	}
	if !holder.Known || holder.PID == 0 {
		return CheckItem{OK: true, Detail: "a daemon holds the lease but could not be identified"}, ""
	}
	if holder.Stale {
		// A wedged holder is checkServeLease's finding, not this one. Reporting
		// it twice, in two vocabularies, makes the real repair harder to find.
		return CheckItem{OK: true, Detail: fmt.Sprintf("pid %d is not heartbeating — see serve_lease", holder.PID)}, ""
	}

	path := gh.DefaultLedgerPath(workspaceRoot)
	since := now.Add(-ledgerCoverageWindow)
	records, err := gh.ReadLedgerSince(path, since)
	// A ledger file that does not exist at all is not an unreadable ledger —
	// it is the loudest form of the gap this arm looks for, so it falls
	// through to the zero-records path below.
	if err != nil && !errors.Is(err, gh.ErrNoLedger) && !os.IsNotExist(err) {
		return CheckItem{OK: true, Detail: fmt.Sprintf("ledger at %s could not be read: %v", path, err)}, ""
	}
	for _, r := range records {
		if r.PID == holder.PID {
			return CheckItem{
				OK: true,
				Detail: fmt.Sprintf("pid %d has written to %s within the last %s",
					holder.PID, path, ledgerCoverageWindow),
			}, ""
		}
	}

	if !pipelineActivitySince(workspaceRoot, since) {
		return CheckItem{
			OK: true,
			Detail: fmt.Sprintf("pid %d has no ledger records in the last %s, and no pipeline "+
				"activity either — an idle daemon spends nothing, so this is not evidence of a gap",
				holder.PID, ledgerCoverageWindow),
		}, ""
	}

	msg := fmt.Sprintf(
		"ledger-daemon-uncovered: pid %d is serving this workspace and a pipeline ran in the "+
			"last %s, but not one of its GitHub calls reached %s. Its spend is therefore "+
			"invisible to `nightgauge api-usage`, which is what makes a rate-limit exhaustion "+
			"unattributable after the fact. The daemon resolves the ledger path from the "+
			"workspace it was told to serve, so a mismatch means it was started against a "+
			"different root: restart it with `nightgauge serve --workspace %s`",
		holder.PID, ledgerCoverageWindow, path, workspaceRoot)
	return CheckItem{
		OK:     false,
		Detail: fmt.Sprintf("pid %d active, zero ledger records in %s", holder.PID, ledgerCoverageWindow),
		Error:  msg,
	}, msg
}

// pipelineActivitySince reports whether any pipeline state file was written
// inside the window — the independent evidence that the daemon had work to do.
func pipelineActivitySince(workspaceRoot string, since time.Time) bool {
	entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".nightgauge", "pipeline"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(since) {
			return true
		}
	}
	return false
}
