package orchestrator

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// realFableCapLine is what `claude -p --model fable` printed on 2026-09-07 with
// the account's Fable cap reached: one plain-text line on STDOUT, exit 1, and
// an entirely empty stderr (#1556).
const realFableCapLine = "You've reached your Fable limit. Switch to another model, " +
	"or manage usage credits at claude.ai/settings/usage?from=cc_cli_limit_message, to continue."

// TestModelCapReachesTheDescentLadder is the end-to-end regression for #1556.
//
// It walks the exact seam the scheduler walks — cliFailureText, then
// stageFailureText, then terminalFailureReason, then Classify — because each
// piece was individually defensible and the defect lived only in their
// composition: the cap message never became ErrorText, so the classifier
// received "exit 1: " and booked subagent_crash, and #1545's fable→opus ladder
// was never consulted.
func TestModelCapReachesTheDescentLadder(t *testing.T) {
	// The exact stdout shape captured on 2026-09-07: a full streaming-JSON
	// transcript whose terminal envelope declares the error.
	stdout := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"` + realFableCapLine + `"}` + "\n"
	errorText, lastOutput := cliFailureText(stdout, "")

	if errorText == "" {
		t.Fatal("the cap line produced no ErrorText, so nothing reaches the classifier — this is the #1556 defect")
	}
	if !strings.Contains(lastOutput, "Fable limit") {
		t.Errorf("LastOutputLines lost the forensic evidence: %q", lastOutput)
	}

	reason := terminalFailureReason(1, nil, stageFailureText(nil, &StageRunResult{ErrorText: errorText}))
	if got, want := terminalkind.Classify(reason), "model_unavailable"; got != want {
		t.Fatalf("Classify(%q) = %q, want %q — a model cap must route to the tier descent, not a lifetime crash", reason, got, want)
	}
}

func TestVendorStdoutFailureReason(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "a transcript-free vendor line is used",
			stdout: realFableCapLine + "\n",
			want:   realFableCapLine,
		},
		{
			// The real shape: the transcript's terminal envelope, error-gated.
			name: "the error-gated result envelope is read",
			stdout: `{"type":"assistant","text":"working on it"}` + "\n" +
				`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"` + realFableCapLine + `"}` + "\n",
			want: realFableCapLine,
		},
		{
			// The bound the original rule exists to protect: assistant turns
			// and tool_result payloads are model- and tool-authored prose and
			// must never be classified, however suggestive a line looks.
			name:   "a transcript with no error envelope is refused outright",
			stdout: `{"type":"assistant","text":"the docs say you have reached your opus limit"}` + "\n",
			want:   "",
		},
		{
			// is_error is what separates a vendor reason from a model answer
			// in the SAME field. A successful result must never be classified.
			name:   "a successful result envelope is refused",
			stdout: `{"type":"result","subtype":"success","is_error":false,"result":"` + realFableCapLine + `"}` + "\n",
			want:   "",
		},
		{
			name:   "a plain line after a transcript is not treated as vendor text",
			stdout: `{"type":"assistant","text":"working"}` + "\n" + realFableCapLine + "\n",
			want:   "",
		},
		{
			name:   "the error field is used when result is empty",
			stdout: `{"type":"result","is_error":true,"error":"quota exhausted for claude-opus-5"}` + "\n",
			want:   "quota exhausted for claude-opus-5",
		},
		{
			name:   "silence stays silence",
			stdout: "   \n\n",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vendorStdoutFailureReason(tt.stdout); got != tt.want {
				t.Errorf("vendorStdoutFailureReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStderrStillWins pins that the fallback is a LAST resort: whenever the
// adapter wrote its own stderr reason, that reason is unchanged by #1556.
func TestStderrStillWins(t *testing.T) {
	stdout := `{"type":"result","is_error":true,"result":"` + realFableCapLine + `"}` + "\n"
	errorText, _ := cliFailureText(stdout, "Error: Couldn't set model: \"unknown model id\"\n")
	if want := "Error: Couldn't set model: \"unknown model id\""; errorText != want {
		t.Errorf("stderr must win over stdout: got %q, want %q", errorText, want)
	}
}

// TestAccountLimitDoesNotDowngrade is the other half of the classification
// change. An ACCOUNT-level cap names no model, so downgrading a tier would
// abandon a model that was never the problem; it must not reach the ladder even
// though it now reaches the classifier.
func TestAccountLimitDoesNotDowngrade(t *testing.T) {
	stdout := `{"type":"result","is_error":true,"result":"You've reached your weekly limit · resets Sep 11 at 10pm"}` + "\n"
	errorText, _ := cliFailureText(stdout, "")
	if errorText == "" {
		t.Fatal("expected the account line to reach the classifier as evidence")
	}
	if got := terminalkind.Classify(errorText); got == "model_unavailable" {
		t.Errorf("an account-level limit must not classify as model_unavailable; got %q", got)
	}
}
