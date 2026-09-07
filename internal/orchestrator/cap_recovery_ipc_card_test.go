package orchestrator

import (
	"strings"
	"testing"
)

// The IPC descent path takes its OWN `continue` (the FallbackRecorded branch)
// well above the cap-recovery block, so a card raised only at that block would
// be absent from the extension dispatch mode — which is exactly the mode #1545's
// harm was reported from. capDescentReason is what keeps the two sites saying
// the same thing.

func TestTheIpcDescentCarriesTheDecisionsOwnSentence(t *testing.T) {
	const decided = "usage cap hit while running claude-fable-5-1 (band fable) on anthropic; descending to opus"
	if got := capDescentReason(decided, "fable", "opus"); got != decided {
		t.Fatalf("reason = %q, want the decision's own sentence carried verbatim", got)
	}
}

// TestAReasonlessDescentStillExplainsItself pins the fallback. A runner that
// reported no reason must still produce a card an operator can read: learning
// "the stage dropped to opus, because of a usage cap" with a thin explanation
// beats learning nothing, which is the state this issue found.
func TestAReasonlessDescentStillExplainsItself(t *testing.T) {
	got := capDescentReason("", "fable", "opus")
	if got == "" {
		t.Fatal("a descent with no reported reason produced no sentence at all")
	}
	for _, want := range []string{"usage cap", "fable", "opus"} {
		if !strings.Contains(got, want) {
			t.Errorf("generic reason %q must name %q", got, want)
		}
	}
	// Whitespace is not a reason.
	if capDescentReason("   ", "fable", "opus") != got {
		t.Error("a blank-but-non-empty reason must fall through to the generic sentence")
	}
}
