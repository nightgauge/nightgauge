package adapters

// opencode's own @opencode-ai/plugin install into an operator-owned config
// directory (#1635/A11, ADR-022 amendment 2026-09-15, narrowed AC1).
//
// Nightgauge never writes into $HOME/.opencode or an inherited
// OPENCODE_CONFIG_DIR. The run's own config directory needs no package: the
// Nightgauge plugin imports only node:* built-ins and its own files, so the
// vendored seed that once stubbed opencode's install there is removed
// (#3306). operatorInstallRisk flags a dispatch touching an operator-owned
// directory that does not already satisfy opencode's own install check, so
// the manager can bound and classify a stage that waits on that install.

import (
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

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
