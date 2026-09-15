package execution

// A Nightgauge plugin gate block (#1635) must never be classified as an
// OpenCode auto-reject: it is a thrown Error from tool.execute.before, never
// OpenCode's own "! permission requested: ...; auto-rejecting" stderr
// notice, so the model sees the gate's reason and the stage continues rather
// than being classified adapter_permission_rejected (ADR-022 § 9,
// TerminalKindAdapterPermissionRejected). This locks in that the two are
// textually disjoint, so openCodeRun.observeStderr — the parser
// TerminalKindAdapterPermissionRejected is read from — never mistakes one
// for the other.

import (
	"fmt"
	"strings"
	"testing"
)

func TestNightgaugeGateMarkerIsNeverAnAutoRejectNotice(t *testing.T) {
	messages := []string{
		"[nightgauge-gate:careful] /careful is ON — blocked: docker compose down -v is a production-destructive Bash command (run `nightgauge careful off` to disable.)",
		"[nightgauge-gate:careful] NIGHTGAUGE_BIN is not set to an absolute path; the careful gate cannot run, so the tool call is blocked closed",
		"[nightgauge-gate:careful] the careful gate timed out or was killed (signal SIGTERM); the tool call is blocked closed",
		"[nightgauge-gate:task-denied] subagent (task) sessions are denied: opencode 1.18.30's tool.execute.before coverage inside a task session is unverified (AC9, ADR-022 amendment 2026-09-14)",
	}
	for _, msg := range messages {
		if strings.HasSuffix(msg, openCodeAutoRejectEnd) {
			t.Fatalf("gate message %q ends in OpenCode's own auto-reject suffix; it would be misread as one line of a real notice", msg)
		}
		if _, ok := openCodeRejectedPermission(msg); ok {
			t.Errorf("openCodeRejectedPermission classified a Nightgauge gate marker as an OpenCode auto-reject notice: %q", msg)
		}
		r := newOpenCodeRun(&OpenCodeStream{}, nil)
		notice, kind := r.observeStderr(msg, msg)
		if kind != stderrKept {
			t.Errorf("observeStderr classified %q as kind %v (notice=%q), want stderrKept", msg, kind, notice)
		}
	}
}

// TestNightgaugeGateToolUseErrorYieldsNoRejectedToolCall is the AC's
// "external-package" bullet: a tool_use stream event whose part.state.error
// carries the plugin's own gate marker must not be counted as one of
// OpenCode's own permission rejections (RejectedToolCalls), which
// ParseOpenCodeStreamLine matches only by exact equality against OpenCode's
// literal rejection string.
func TestNightgaugeGateToolUseErrorYieldsNoRejectedToolCall(t *testing.T) {
	acc := &TokenAccumulator{}
	line := fmt.Sprintf(`{"type":"tool_use","sessionID":"s1","part":{"tool":"bash","state":{"status":"error","error":%q}}}`,
		"[nightgauge-gate:careful] /careful is ON — blocked: docker compose down -v is a production-destructive Bash command")
	event, _ := acc.ParseOpenCodeStreamLine(line)
	if event == nil || event.Type != "tool_use" {
		t.Fatalf("the fixture line did not parse as a tool_use event: %+v", event)
	}
	if got := acc.OpenCode().RejectedToolCalls; got != 0 {
		t.Errorf("RejectedToolCalls = %d, want 0: a Nightgauge gate block must never be counted as an OpenCode permission rejection", got)
	}
}
