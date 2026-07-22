package orchestrator

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// configReloadInterval is the polling cadence for the machine-tier config
// watcher. Edits to ~/.nightgauge/config.yaml are rare, so polling at
// this rate has negligible cost (one os.Stat per tick) while keeping the
// dependency surface free of fsnotify.
var configReloadInterval = 3 * time.Second

// configReloader is the minimal surface from *AutonomousScheduler that the
// watcher needs. Defined as an interface so tests can supply a fake.
type configReloader interface {
	FilterRepos(workspaceRepos []string)
	IsRunning() bool
}

// WatchAutonomousConfig polls the machine-tier config file (typically
// ~/.nightgauge/config.yaml) and, whenever its mtime changes,
// re-applies the resolved `autonomous.enabled_repos` allowlist to the
// running scheduler.
//
// Without this, direct edits to the config file silently no-op until the
// user restarts autonomous or toggles a repo checkbox in the extension's
// Repositories view (which fires `autonomous.updateAllowlist` over IPC).
//
// Returns immediately if the config path cannot be resolved (no $HOME).
// The watcher exits when ctx is cancelled.
func WatchAutonomousConfig(ctx context.Context, sched configReloader, workspaceRoot string) {
	path, err := config.MachineConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[nightgauge] autonomous config watcher disabled: %v\n", err)
		return
	}

	var lastMtime time.Time
	if info, statErr := os.Stat(path); statErr == nil {
		lastMtime = info.ModTime()
	}

	ticker := time.NewTicker(configReloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, statErr := os.Stat(path)
			if statErr != nil {
				// File missing or unreadable — drop watch state so a
				// future create is treated as a fresh change.
				lastMtime = time.Time{}
				continue
			}
			if !info.ModTime().After(lastMtime) {
				continue
			}
			lastMtime = info.ModTime()

			if !sched.IsRunning() {
				continue
			}

			cfg, loadErr := config.Load(workspaceRoot)
			if loadErr != nil || cfg == nil || cfg.Autonomous == nil {
				continue
			}
			enabled := cfg.Autonomous.ResolvedEnabledRepos(cfg.Owner)
			if len(enabled) == 0 {
				// Empty allowlist means "no machine-tier restriction" —
				// the IPC layer treats this as "defer to workspaceRepos",
				// which the watcher cannot reconstruct on its own. Skip
				// rather than risk widening the scheduler beyond the
				// user's original autonomous.start payload.
				continue
			}
			sched.FilterRepos(enabled)
			fmt.Fprintf(os.Stderr, "[nightgauge] autonomous.enabled_repos reapplied from config change: %v\n", enabled)
		}
	}
}
