package adapters

// opencode 1.18.30's own dependency install for a plugin-bearing config
// (#1635 fix round, ADR-022 amendment 2026-09-14, #1632 row 1; #1635/A11
// round 6, ADR-022 amendment 2026-09-15, "operator directories are never
// merged into", narrowed AC1).
//
// The moment a resolved OpenCode config's `plugin` array is non-empty —
// regardless of whether an entry is a local file with no import of its own —
// 1.18.30 installs the @opencode-ai/plugin npm package into that config
// directory's node_modules, and every invocation that resolves the config
// (`debug config`, `run`, ...) waits for that install before doing anything
// else. 1.18.30 does this for every config directory its resolved config
// touches, not only the run's own: $HOME/.opencode when it exists, and,
// under opencode.inherit_user_config, the operator's own OPENCODE_CONFIG_DIR
// too.
//
// The first fix round replaced a live `npm install` (unpinned, lifecycle
// scripts enabled, a real registry round trip on every cold cache — the
// #1635 review's findings 2-4) with opencodeplugin.WriteDependencies: an
// embedded, version-pinned copy of @opencode-ai/plugin@1.18.30 (DepsVersion,
// matching the tested opencode version) captured once and extracted, never
// installed — no npm binary is looked up, no lifecycle script runs and no
// network request is ever made. That extraction is a truncating one, drawn
// from a four-file stub with no dist/ (opencodeplugin's package doc
// comment): safe ONLY for the run's own OpenCode config directory, which
// this run just created and owns outright and where only the embedded
// Nightgauge plugin ever loads.
//
// A later round tried extending that seed — first the same stub, then a
// second, ~10.4 MB re-embedded real tree — into $HOME/.opencode and an
// inherited OPENCODE_CONFIG_DIR, directories an operator may already hold
// their own package.json, lockfiles, other packages and tool/plugin files
// that import @opencode-ai/plugin in. Round 6 reverses that: Nightgauge
// never writes into either directory. OpenCode's own install into its own
// config directories is the operator's environment, exactly as in the
// operator's own OpenCode runs. seedPluginDependencies (below) seeds only
// the run's own directory; operatorInstallRisk flags a dispatch touching an
// operator-owned directory that does not already satisfy the pin, so the
// manager can bound and classify a stage that waits on OpenCode's own
// install there (manager.go). Round 7 dropped that exemption on a
// measurement that checked only the version marker rather than the full set
// opencode's own check reads; round 8 (ADR-022 amendment 2026-09-15)
// restores it once OperatorInstallSatisfied itself checks the full set — see
// operatorInstallRisk's own doc comment for the corrected measurement.
//
// pluginDependencySeeder is a package-level var, not a direct call, so a
// unit test can replace it with a stub that touches no filesystem at all
// (#1635 review finding 4: a unit test must never make a registry request -
// extended here to never even pay the real extraction's cost). Real
// dispatches and the opencode_integration suite use the *Embedded* default.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// pluginDependencySeeder installs InstallNightgaugePlugin's dependency
// pre-seed into a run's own, freshly-created OpenCode config directory.
// Overridable per test (restore in t.Cleanup); the zero value is never used
// directly — every caller goes through this var.
var pluginDependencySeeder = writeEmbeddedPluginDependencies

// writeEmbeddedPluginDependencies is pluginDependencySeeder's real
// implementation: a pure, local archive extraction
// (opencodeplugin.WriteDependencies), honouring ctx by checking it before
// doing any work — extraction itself is local filesystem I/O with no
// network wait to cancel mid-flight, so the check is what keeps a dispatch
// whose context is already done from starting it at all, not a mid-extract
// abort. configDir is the run's own OpenCode config directory (the parent of
// PluginDir) — the exact directory 1.18.30 installs @opencode-ai/plugin
// into once the per-run config's `plugin` array is non-empty.
//
// Best-effort by contract with the caller (InstallNightgaugePlugin): an
// error here must never fail a dispatch. A directory this leaves unseeded is
// no worse than today's behaviour without it — opencode's own install still
// runs for whatever is missing, exactly as if this seed did not exist.
func writeEmbeddedPluginDependencies(ctx context.Context, configDir string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("opencode: plugin dependency seed: %w", err)
	}
	if err := opencodeplugin.WriteDependencies(configDir); err != nil {
		return fmt.Errorf("opencode: plugin dependency seed: %w", err)
	}
	return nil
}

// seedPluginDependencies is InstallNightgaugePlugin's pre-spawn hook for the
// run's own config directory. Errors are swallowed here, not just inside
// pluginDependencySeeder's default: a seed failure of any kind (a stub test
// double included) must never turn into a dispatch failure, but it is
// logged — a directory 1.18.30 then finds unsatisfied falls back to its own
// install, which is worth an operator's attention (#1635 fix round 2 review,
// "a failed seed silently falls back to opencode's own npm install").
func seedPluginDependencies(ctx context.Context, configDir string) {
	if err := pluginDependencySeeder(ctx, configDir); err != nil {
		log.Printf("opencode: could not pre-seed %s with the embedded @opencode-ai/plugin copy; "+
			"opencode's own install may run for it instead: %v", configDir, err)
	}
}

// operatorInstallRisk reports, READ-ONLY, the operator-owned OpenCode config
// directory run's config puts at risk of OpenCode's own @opencode-ai/plugin
// install waiting on the registry — "" when none applies. Nightgauge never
// seeds or merges into either directory (#1635/A11 round 6, ADR-022
// amendment 2026-09-15, narrowed AC1): OpenCode's own install into its own
// config directories is the operator's environment, exactly as in the
// operator's own OpenCode runs.
//
//   - $HOME/.opencode, where $HOME is what this dispatch's own env sets it
//     to (run.Env["HOME"]), falling back to run.Home when the run leaves HOME
//     untouched (opencode.inherit_user_config on) — only when it already
//     exists AND does not already satisfy the pin — opencode never creates it
//     itself, so an absent one is not this dispatch's problem. A
//     non-inheriting run's HOME is the per-run root's home/ (#1787), whose
//     .opencode linkOperatorHome never populates, so this can only ever flag
//     the operator's real $HOME/.opencode under the inherit setting.
//   - OPENCODE_CONFIG_DIR: whenever the run's env sets it and it does not
//     already satisfy the pin (only ever set under
//     opencode.inherit_user_config) — an absent one is unsatisfied by
//     construction (opencodeplugin.OperatorInstallSatisfied requires the
//     files to exist), so it is still flagged: opencode creates and installs
//     into it itself the moment a config naming it resolves, whether or not
//     it existed before this dispatch.
//
// A directory opencodeplugin.OperatorInstallSatisfied reports satisfied is
// NOT flagged (#1635/A11 round 8, ADR-022 amendment 2026-09-15, correcting
// round 7; the fix round after it corrected OperatorInstallSatisfied's own
// predicate to match opencode's real Npm.install check — see its doc
// comment in deps.go). Round 7 flagged a satisfied directory too, on the
// theory that resolving a plugin-bearing config against $HOME/.opencode or
// OPENCODE_CONFIG_DIR "pays the same ~70-80s registry round trip regardless
// of whether node_modules already satisfies the pin" — but that measurement
// checked only the version marker, which opencode's own "is
// @opencode-ai/plugin already installed" check does not read at all
// (deps.go's OperatorInstallSatisfied doc comment). Driven directly against
// the real 1.18.30 binary with the predicate OperatorInstallSatisfied now
// checks, a satisfied operator directory gets the same local, instant fast
// path (~1s for `debug config`, ~4.5s for a whole dispatch) a run's own
// XDG-resolved config directory always did — round 7's own re-measurement
// conflated the marker-only case (still slow) with the fully-satisfied case
// (fast), and its conclusion does not hold. manager.go's watchdog this flag
// arms also stands down the instant the directory BECOMES satisfied, polled
// read-only, not only on first output — an operator's OWN in-flight install
// completing must never be capped as if it were a hang.
func operatorInstallRisk(run *OpenCodeRun) string {
	dispatchHome := run.Env["HOME"]
	if dispatchHome == "" {
		dispatchHome = run.Home
	}
	if dispatchHome != "" {
		home := filepath.Join(dispatchHome, ".opencode")
		if fi, err := os.Stat(home); err == nil && fi.IsDir() && !opencodeplugin.OperatorInstallSatisfied(home) {
			return home
		}
	}
	if dir := run.Env["OPENCODE_CONFIG_DIR"]; dir != "" && !opencodeplugin.OperatorInstallSatisfied(dir) {
		return dir
	}
	return ""
}
