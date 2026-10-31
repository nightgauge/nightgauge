package execution

import (
	"fmt"
	"os"

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
}

// EvaluateClaudePluginInit judges a system/init event. A nil event means work
// arrived on the stream with no init before it.
func EvaluateClaudePluginInit(bundled bool, event *StreamEvent) ClaudePluginReading {
	reason := ""
	version := ""
	switch {
	case event == nil:
		reason = "the stream carried work before any system/init event"
	case event.Plugins == nil:
		reason = "system/init has no plugins list"
	default:
		loaded := 0
		for _, p := range event.Plugins {
			if p.Name == claudePluginName {
				loaded++
				version = p.Version
			}
		}
		switch loaded {
		case 1:
			return ClaudePluginReading{Verdict: ClaudePluginPass, Version: version}
		case 0:
			reason = "system/init lists no nightgauge plugin"
		default:
			version = ""
			reason = fmt.Sprintf("system/init lists %d nightgauge plugins, so each gate would fire more than once", loaded)
		}
	}
	if bundled {
		return ClaudePluginReading{Verdict: ClaudePluginStop, Reason: reason}
	}
	return ClaudePluginReading{Verdict: ClaudePluginWarn, Reason: reason}
}

// claudePluginGate applies EvaluateClaudePluginInit to one stage's stream. Like
// the OpenCode handshake state it is written only by the stdout goroutine and
// read after wg.Wait(). A nil gate, for every other adapter, observes nothing.
type claudePluginGate struct {
	bundled bool
	decided bool
	reading ClaudePluginReading
}

func newClaudePluginGate(bundled bool) *claudePluginGate {
	return &claudePluginGate{bundled: bundled}
}

// observe reports true on the one event that stops the stage. Only the first
// init counts: a later one cannot undo a stage that already ran.
func (g *claudePluginGate) observe(event *StreamEvent) bool {
	if g == nil || g.decided || event == nil {
		return false
	}
	switch {
	case event.Type == "system" && event.Subtype == "init":
		g.reading = EvaluateClaudePluginInit(g.bundled, event)
	case isStreamWork(event):
		g.reading = EvaluateClaudePluginInit(g.bundled, nil)
	default:
		// hook_started precedes init on a real stream, so other system events
		// and heartbeats decide nothing.
		return false
	}
	g.decided = true
	return g.reading.Verdict == ClaudePluginStop
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
		return fmt.Sprintf("%s stopped before any work: %s; this build's safety gates did not load",
			gatePluginMissingMarker, g.reading.Reason)
	case ClaudePluginWarn:
		return fmt.Sprintf("%s %s; this build has no bundled plugin, so the stage continues without confirmed gates",
			gatePluginWarningMarker, g.reading.Reason)
	}
	return ""
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
