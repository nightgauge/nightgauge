package execution

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// The stage gate that proves a Claude stage has its safety gates (#2723).
//
// careful-gate, stage-gate, workflow-gate, stop-verification and test-quality
// ship in the Nightgauge plugin. The stage asks for it with --plugin-dir, and the
// CLI's system/init event is the only first-hand report of what loaded. A gate a
// run can lose without noticing is not a gate, so a bundled build reads that
// event as fail-closed: every ambiguous reading stops the stage. A build with no
// bundle has nothing to hold the CLI to, so it warns and runs.
//
// claudePluginGate.ts is the extension path's twin of this file.
const (
	// claudePluginName is the plugin's manifest name, matched exactly.
	claudePluginName = "nightgauge"
	// gatePluginMissingMarker ends a stopped stage's stderr, and the
	// gate-plugin-missing terminal rule classifies it.
	gatePluginMissingMarker = "[gate-plugin-missing]"
	// gatePluginWarningMarker is deliberately a different literal: a warning
	// that survived into a later failure's text must not park that failure.
	gatePluginWarningMarker = "[gate-plugin-warning]"
)

// ClaudePluginVerdict is what a stage does about one system/init reading.
type ClaudePluginVerdict int

const (
	// ClaudePluginPass: exactly one Nightgauge plugin loaded.
	ClaudePluginPass ClaudePluginVerdict = iota
	// ClaudePluginWarn: it did not, on a build with no bundle.
	ClaudePluginWarn
	// ClaudePluginStop: it did not, on a bundled build.
	ClaudePluginStop
)

// ClaudePluginReading is one evaluated system/init.
type ClaudePluginReading struct {
	Verdict ClaudePluginVerdict
	// Version is the loaded plugin's version, "" unless exactly one loaded.
	Version string
	// Reason names what was wrong, "" on a pass.
	Reason string
	// CLIVersion is the init's claude_code_version, "" when it named none or
	// no init arrived.
	CLIVersion string
}

// EvaluateClaudePluginInit judges a system/init event. A nil event means work
// arrived on the stream with no init before it. bundledDir is the plugin the
// stage was given; on a bundled build the one nightgauge entry must have loaded
// from it, since the entry's source (nightgauge@inline for any --plugin-dir
// load) cannot say which copy ran (#2883). A bundled build with no bundledDir
// is stamped but damaged, and stops.
func EvaluateClaudePluginInit(bundled bool, bundledDir string, event *StreamEvent) ClaudePluginReading {
	reason := ""
	cliVersion := ""
	if event != nil {
		cliVersion = event.ClaudeCodeVersion
	}
	switch {
	case bundled && bundledDir == "":
		reason = "this build is stamped as bundled but its plugin at <prefix>/claude-plugins/nightgauge has no hooks/hooks.json"
	case event == nil:
		reason = "the stream carried work before any system/init event"
	case event.Plugins == nil:
		reason = "system/init has no plugins list"
	default:
		var found []StreamPlugin
		for _, p := range event.Plugins {
			if p.Name == claudePluginName {
				found = append(found, p)
			}
		}
		switch len(found) {
		case 1:
			p := found[0]
			if !bundled || samePluginPath(p.Path, bundledDir) {
				return ClaudePluginReading{Verdict: ClaudePluginPass, Version: p.Version, CLIVersion: cliVersion}
			}
			reason = fmt.Sprintf("system/init lists nightgauge from %s at %s, not the bundled plugin at %s",
				orUnnamed(p.Source), orUnnamed(p.Path), bundledDir)
		case 0:
			reason = "system/init lists no nightgauge plugin"
		default:
			reason = fmt.Sprintf("system/init lists %d nightgauge plugins, so each gate would fire more than once", len(found))
		}
	}
	verdict := ClaudePluginWarn
	if bundled {
		verdict = ClaudePluginStop
	}
	return ClaudePluginReading{Verdict: verdict, Reason: reason, CLIVersion: cliVersion}
}

func orUnnamed(s string) string {
	if s == "" {
		return "an unnamed location"
	}
	return s
}

// samePluginPath compares two plugin directories after cleaning and, where
// they resolve, following symlinks.
func samePluginPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	norm := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	return norm(a) == norm(b)
}

// claudePluginGate applies EvaluateClaudePluginInit to one stage's stream. Like
// the OpenCode handshake state it is written only by the stdout goroutine and
// read after wg.Wait(). A nil gate, for every other adapter, observes nothing.
type claudePluginGate struct {
	bundled bool
	dir     string
	decided bool
	reading ClaudePluginReading
}

func newClaudePluginGate(bundled bool, dir string) *claudePluginGate {
	return &claudePluginGate{bundled: bundled, dir: dir}
}

// observe reports true on the one event that stops the stage. Only the first
// init counts: a later one cannot undo a stage that already ran.
func (g *claudePluginGate) observe(event *StreamEvent) bool {
	if g == nil || g.decided || event == nil {
		return false
	}
	switch {
	case event.Type == "system" && event.Subtype == "init":
		g.reading = EvaluateClaudePluginInit(g.bundled, g.dir, event)
	case isStreamWork(event):
		g.reading = EvaluateClaudePluginInit(g.bundled, g.dir, nil)
	default:
		// hook_started precedes init on a real stream, so other system events
		// and heartbeats decide nothing.
		return false
	}
	g.decided = true
	return g.reading.Verdict == ClaudePluginStop
}

// observeLine runs observe on a raw stream line, ahead of the stage's accumulator.
// ParseLine books as it parses, so the gate reads through a throwaway accumulator
// and a line that stops the stage is never booked (#2807).
func (g *claudePluginGate) observeLine(format AdapterStreamFormat, line string) bool {
	if g == nil || g.decided {
		return false
	}
	event, _ := (&TokenAccumulator{}).ParseLine(format, line)
	return g.observe(event)
}

// isStreamWork reports an event only a running session produces.
func isStreamWork(event *StreamEvent) bool {
	switch event.Type {
	case "assistant", "user", "result", "tool_use", "stream_event",
		"content_block_start", "content_block_delta", "message_start":
		return true
	}
	return false
}

func (g *claudePluginGate) stopped() bool {
	return g != nil && g.decided && g.reading.Verdict == ClaudePluginStop
}

// version is the plugin version to record, "" when none loaded.
func (g *claudePluginGate) version() string {
	if g == nil {
		return ""
	}
	return g.reading.Version
}

// notice is the stderr line for this stage: the stop marker, a warning, or "".
func (g *claudePluginGate) notice() string {
	if g == nil || !g.decided {
		return ""
	}
	switch g.reading.Verdict {
	case ClaudePluginStop:
		return fmt.Sprintf("%s stopped before any work: %s; this build's safety gates did not load%s",
			gatePluginMissingMarker, g.reading.Reason, cliFloorClause(g.reading.CLIVersion))
	case ClaudePluginWarn:
		return fmt.Sprintf("%s %s; this build has no bundled plugin, so the stage continues without confirmed gates",
			gatePluginWarningMarker, g.reading.Reason)
	}
	return ""
}

// cliFloorClause names the supported Claude CLI floor when the init's CLI is
// below it or reported no version, "" otherwise (#2883).
func cliFloorClause(cliVersion string) string {
	floor, below := adaptercompat.BelowFloor("claude-headless", cliVersion)
	if !below {
		return ""
	}
	detected := "Claude CLI " + cliVersion
	if cliVersion == "" {
		detected = "Claude CLI reported no version and"
	}
	return fmt.Sprintf("; %s is below the supported floor %s: upgrade Claude Code to %s or later",
		detected, floor, floor)
}

// resolveClaudePluginStamp reports whether this build carries the bundle stamp.
func (m *Manager) resolveClaudePluginStamp() bool {
	if m.claudePluginStamp != nil {
		return m.claudePluginStamp()
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return skillrender.ClaudePluginBundleStamped(exe)
}

// resolveClaudePluginDir returns the plugin directory a Claude stage loads, ""
// when this build has no bundle.
func (m *Manager) resolveClaudePluginDir() string {
	if m.claudePluginDir != nil {
		return m.claudePluginDir()
	}
	return bundledClaudePluginDir()
}

func bundledClaudePluginDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return skillrender.BundledClaudePluginDir(exe)
}
