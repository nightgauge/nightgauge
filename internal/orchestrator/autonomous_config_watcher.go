package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
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

// repoSetReloader adds manifest-driven repo-set replacement (#704). Kept
// separate from configReloader so an implementation that only supports the
// machine-tier allowlist still satisfies the original contract.
type repoSetReloader interface {
	configReloader
	ReplaceRepos(repos []depgraph.RepoConfig) bool
}

// ManifestRepoResolver re-resolves the workspace manifest into a repo list.
//
// Injected rather than called directly: the resolver lives in package main
// (it needs the same owner/project defaults the daemon started with), and
// internal/orchestrator cannot import upward. Returning nil or an empty slice
// means "could not read the manifest" and is treated as a no-op, never as
// "the workspace is now empty".
type ManifestRepoResolver func() []depgraph.RepoConfig

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
	WatchAutonomousConfigWithManifest(ctx, sched, workspaceRoot, nil)
}

// WatchAutonomousConfigWithManifest is WatchAutonomousConfig plus a second
// watch on the workspace manifest (#704).
//
// The extension hot-reloads `.vscode/nightgauge-workspace.yaml`, so without
// this the tree would list a repository the running scheduler is not
// processing — a silent divergence between two surfaces that is expensive to
// diagnose. Both files are polled on the same ticker, keeping the design (and
// the absence of an fsnotify dependency) this file already committed to.
//
// resolveRepos may be nil, in which case only the machine-tier config is
// watched and behavior is identical to before.
func WatchAutonomousConfigWithManifest(
	ctx context.Context,
	sched configReloader,
	workspaceRoot string,
	resolveRepos ManifestRepoResolver,
) {
	path, err := config.MachineConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[nightgauge] autonomous config watcher disabled: %v\n", err)
		return
	}

	manifestPath := filepath.Join(workspaceRoot, ".vscode", "nightgauge-workspace.yaml")
	repoSetSched, canReplaceRepos := sched.(repoSetReloader)
	watchManifest := resolveRepos != nil && canReplaceRepos

	var lastMtime time.Time
	if info, statErr := os.Stat(path); statErr == nil {
		lastMtime = info.ModTime()
	}
	var lastManifestMtime time.Time
	if info, statErr := os.Stat(manifestPath); statErr == nil {
		lastManifestMtime = info.ModTime()
	}

	ticker := time.NewTicker(configReloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if watchManifest {
				checkManifest(manifestPath, &lastManifestMtime, repoSetSched, resolveRepos)
			}

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

// checkManifest re-resolves the workspace manifest when its mtime advances and
// hands the result to the scheduler.
//
// Deliberately tolerant: a manifest that is missing, unreadable, or mid-edit
// resolves to nothing, and ReplaceRepos treats an empty resolution as a no-op.
// Narrowing the scheduler to zero repos because a save landed on a half-written
// file would stop all scanning until restart — strictly worse than briefly
// running against the previous set.
func checkManifest(
	path string,
	lastMtime *time.Time,
	sched repoSetReloader,
	resolveRepos ManifestRepoResolver,
) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		// Missing or unreadable: drop watch state so a later create counts as
		// a change. The repo set is left alone.
		*lastMtime = time.Time{}
		return
	}
	if !info.ModTime().After(*lastMtime) {
		return
	}
	*lastMtime = info.ModTime()

	if !sched.IsRunning() {
		return
	}

	repos := resolveRepos()
	if len(repos) == 0 {
		return
	}
	if sched.ReplaceRepos(repos) {
		names := make([]string, 0, len(repos))
		for _, r := range repos {
			names = append(names, r.FullName())
		}
		fmt.Fprintf(os.Stderr, "[nightgauge] workspace manifest changed: repo set is now %v\n", names)
	}
}
