package orchestrator

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// escalationWriteSites are the two files that decide a model escalation. The
// list is the whole point of the test below, so it is spelled out rather than
// discovered: a walk that found "every file mentioning RecordEscalation" would
// grow silently to include tests and helpers, and stop meaning anything.
var escalationWriteSites = []string{
	filepath.Join("scheduler.go"),
	filepath.Join("..", "ipc", "ipc_stage_runner.go"),
}

var (
	recordEscalationCall = regexp.MustCompile(`\.RecordEscalation\(`)
	appendEscalationCall = regexp.MustCompile(`\.AppendEscalation\(`)
)

// TestEveryRecordEscalationHasADurableTwin is the mechanism behind #463.
//
// RetryEngine.RecordEscalation increments a counter in process memory. Nothing
// persists it, nothing snapshots it, and nothing outside the engine reads it —
// it exists to enforce the per-stage escalation ceiling and no more. The
// DURABLE record of an escalation is RuntimeState.AppendEscalation, and until
// #463 only the two model-unavailable DOWNGRADE sites called it. Every upward
// escalation therefore happened without leaving a trace on the run:
// AttemptsUntilSuccess (which sums len(EscalationHistory)) undercounted, the
// stage's model was attributed to "scheduler" as though nothing had substituted
// it, and the "escalation" member of the model-selection vocabulary was
// unreachable.
//
// The behavioural tests in internal/state and internal/ipc cover the sites that
// exist today. This one covers the site somebody adds TOMORROW: a new escalation
// branch that calls only RecordEscalation compiles, passes every other test, and
// silently reopens the defect. Counting is crude on purpose — it cannot check
// that the pairs are in the same branch — but it is the check that fails when a
// site is added with only half of the pair, which is the failure that actually
// happened.
func TestEveryRecordEscalationHasADurableTwin(t *testing.T) {
	totalRecord := 0

	// PER FILE, not summed. A repo-wide total lets one file's surplus mask
	// another's deficit — and it did: scheduler.go carries two downgrade
	// appends with no matching RecordEscalation, which is exactly enough
	// slack to hide the IPC runner writing none at all.
	for _, path := range escalationWriteSites {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v\n"+
				"This test is path-coupled to the escalation write sites. If the file "+
				"moved, move the entry in escalationWriteSites — do NOT delete it; a "+
				"deleted entry is an unwatched site.", path, err)
		}
		records := len(recordEscalationCall.FindAllString(string(source), -1))
		appends := len(appendEscalationCall.FindAllString(string(source), -1))
		totalRecord += records
		t.Logf("%s: %d RecordEscalation, %d AppendEscalation", path, records, appends)

		// AppendEscalation legitimately runs AHEAD within a file: the two
		// model-unavailable downgrade sites append without a RecordEscalation,
		// because a downgrade is not an escalation as far as the retry engine's
		// ceiling is concerned. What must never happen is the reverse — an
		// escalation the run does not record.
		if appends < records {
			t.Errorf("%s: %d RecordEscalation call(s) but only %d AppendEscalation call(s).\n"+
				"At least one escalation site bumps the in-memory retry counter without "+
				"writing a durable EscalationRecord, so that escalation is invisible to "+
				"AttemptsUntilSuccess, to model_selection.source, and to every reader of "+
				"the history record. Pair the new site with an AppendEscalation carrying "+
				"FromModel, ToModel, a Reason from state.EscalationReasons, and At.",
				path, records, appends)
		}
	}

	if totalRecord == 0 {
		t.Fatal("found no RecordEscalation calls at all — escalationWriteSites has drifted " +
			"away from the code it is supposed to watch, so this test is checking nothing")
	}
}
