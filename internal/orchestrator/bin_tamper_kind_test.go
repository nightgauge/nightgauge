package orchestrator

import "testing"

// The manager's stage-binary verification (#3688) fails a stage with the
// `[bin-tamper]` marker; the reason classifies to bin_tamper whatever else
// the stage printed, parks, and names the operator's next step.
func TestBinTamperClassifiesAndParks(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{
		"acme/repo#3688 feature-dev: [bin-tamper] bin_tamper: the stage's NIGHTGAUGE_BIN copy /r/bin/nightgauge changed: sha256 differ",
		"exit 0: overloaded, retrying; API Error: connection closed\n[bin-tamper] bin_tamper: the stage's NIGHTGAUGE_BIN copy changed: the copy could not be read: lstat: no such file or directory",
	} {
		if got := ClassifyTerminalKind(reason); got != TerminalKindBinTamper {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", reason, got, TerminalKindBinTamper)
		}
	}
	if !TerminalKindParks(TerminalKindBinTamper) {
		t.Error("bin_tamper does not park; a retry would rerun a stage that rewrote its own gates")
	}
	if TerminalKindRemediation(TerminalKindBinTamper) == "" {
		t.Error("bin_tamper has no remediation for the operator")
	}
}
