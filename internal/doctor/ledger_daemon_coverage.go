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
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// ledgerCoverageWindow is how far back the arm looks for the daemon's own
// records. One heartbeat interval is the shortest window in which a live
// daemon is definitely doing something; anything shorter would fire on the
// gap between two sweeps.
var ledgerCoverageWindow = runstate.ServeHeartbeatInterval

// ledgerDaemonCoverageFindings reports a live, active daemon whose GitHub traffic
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
func ledgerDaemonCoverageFindings(workspaceRoot string, now time.Time) ([]Finding, string) {
	if workspaceRoot == "" {
		return nil, "ledger coverage not checked (no workspace root)"
	}

	holder, held := runstate.InspectServeLease(workspaceRoot)
	if !held {
		return nil, "no daemon is serving this workspace"
	}
	if !holder.Known || holder.PID == 0 {
		return nil, "a daemon holds the lease but could not be identified"
	}
	if holder.Stale {
		// A wedged holder is the serve_lease finding, not this one. Reporting
		// it twice, in two vocabularies, makes the real repair harder to find.
		return nil, fmt.Sprintf("pid %d is not heartbeating — see serve_lease", holder.PID)
	}

	path, err := gh.DefaultLedgerPath(workspaceRoot)
	if err != nil {
		return nil, fmt.Sprintf("the ledger could not be located: %v", err)
	}
	since := now.Add(-ledgerCoverageWindow)
	records, err := gh.ReadLedgerSince(path, since)
	// A ledger file that does not exist at all is not an unreadable ledger —
	// it is the loudest form of the gap this arm looks for, so it falls
	// through to the zero-records path below.
	if err != nil && !errors.Is(err, gh.ErrNoLedger) && !os.IsNotExist(err) {
		return nil, fmt.Sprintf("ledger at %s could not be read: %v", path, err)
	}
	for _, r := range records {
		if r.PID == holder.PID {
			return nil, fmt.Sprintf("pid %d has written to %s within the last %s",
				holder.PID, path, ledgerCoverageWindow)
		}
	}

	if !pipelineActivitySince(workspaceRoot, since) {
		return nil, fmt.Sprintf("pid %d has no ledger records in the last %s, and no pipeline "+
			"activity either — an idle daemon spends nothing, so this is not evidence of a gap",
			holder.PID, ledgerCoverageWindow)
	}

	const check, code = "ledger_daemon_coverage", "NGD023"
	restart := "nightgauge serve --workspace " + workspaceRoot
	return []Finding{newFinding(check, code, SeverityWarning,
			fmt.Sprintf("ledger-daemon-uncovered: pid %d's GitHub calls are missing from the ledger", holder.PID),
			fmt.Sprintf("pid %d is serving this workspace and a pipeline ran in the "+
				"last %s, but not one of its GitHub calls reached %s. Its spend is therefore "+
				"invisible to `nightgauge api-usage`, which is what makes a rate-limit exhaustion "+
				"unattributable after the fact. The daemon resolves the ledger path from the "+
				"workspace it was told to serve, so a mismatch means it was started against a "+
				"different root: restart it with `%s`",
				holder.PID, ledgerCoverageWindow, path, restart),
			map[string]string{"pid": fmt.Sprint(holder.PID), "ledger": path,
				"workspace": workspaceRoot, "window": ledgerCoverageWindow.String()},
			[]string{path},
			manualRemedy("restart", "Restart the daemon against this workspace", check,
				"Stop the daemon (pid "+fmt.Sprint(holder.PID)+")",
				"Run `"+restart+"`"))},
		fmt.Sprintf("pid %d active, zero ledger records in %s", holder.PID, ledgerCoverageWindow)
}

// pipelineActivitySince reports whether any pipeline state file was written
// inside the window — the independent evidence that the daemon had work to do.
func pipelineActivitySince(workspaceRoot string, since time.Time) bool {
	dir := pipelineStatePath(workspaceRoot, "")
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
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
