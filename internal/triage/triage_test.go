package triage

import (
	"strings"
	"testing"
	"time"
)

// grounded is a record that meets the contract, used as the baseline every
// negative case mutates exactly one field of. Building the failures by
// subtraction from a passing record is what proves each rule is individually
// load-bearing rather than incidentally satisfied by the others.
func grounded() Record {
	return Record{
		V:      SchemaVersion,
		ID:     "e2e-sweep-20260901T120000Z",
		Target: Target{Kind: "check", Value: "E2E Sweep", Repo: "owner/app"},
		History: History{Checked: true, EverPassed: false,
			Detail: "gh run list --workflow e2e.yml --limit 60: zero conclusions=success"},
		Reproduction: Reproduction{
			Status:   ReproLocal,
			Command:  "flutter test --tags=app-e2e integration_test/app_e2e/setup_flow_test.dart",
			Evidence: "pumpAndSettle timed out after 10m; the app bar's progress indicator never stopped",
		},
		Hypotheses: []Hypothesis{
			{Statement: "the magic link is not being delivered", Verdict: VerdictFalsified,
				Observation: "mailcatcher shows the link delivered 1.2s after the request, and the tap handler fired"},
			{Statement: "an indeterminate progress indicator keeps the frame dirty forever",
				Verdict:     VerdictSupported,
				Observation: "widget tree dump during the hang shows LinearProgressIndicator with value:null while the outbox has a pending row"},
		},
		Fix: &Fix{
			Landed: true, Branch: "fix/settle-on-outbox", PR: "owner/app#380",
			Test:                "integration_test/app_e2e/setup_flow_test.dart::signs in from settings",
			TestFailsWithoutFix: true,
		},
		TrackingIssue: "owner/app#379",
		CreatedAt:     "2026-09-01T12:00:00Z",
	}
}

func fieldsOf(vs []Violation) string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Field)
	}
	return strings.Join(out, ",")
}

func TestValidate_GroundedRecordPasses(t *testing.T) {
	if vs := grounded().Validate(); len(vs) != 0 {
		t.Fatalf("expected no violations, got %s", fieldsOf(vs))
	}
}

// TestValidate_RequiresAFalsifiedHypothesis is the contract's centre. A report
// naming only the winning explanation is indistinguishable from a plausible
// guess — and a plausible guess is what shipped twice before this existed.
func TestValidate_RequiresAFalsifiedHypothesis(t *testing.T) {
	rec := grounded()
	rec.Hypotheses = []Hypothesis{rec.Hypotheses[1]} // keep only the winner
	vs := rec.Validate()
	if len(vs) == 0 {
		t.Fatal("a record naming only its winning hypothesis must not validate")
	}
	if !strings.Contains(fieldsOf(vs), "hypotheses") {
		t.Errorf("violations = %s, want one on hypotheses", fieldsOf(vs))
	}
}

// TestValidate_FalsifiedNeedsAnObservation — "I decided it was not that" is
// exactly the move the record exists to prevent, and it is indistinguishable
// from a real ruling-out unless the observation is written down.
func TestValidate_FalsifiedNeedsAnObservation(t *testing.T) {
	rec := grounded()
	rec.Hypotheses[0].Observation = ""
	vs := rec.Validate()
	if !strings.Contains(fieldsOf(vs), "hypotheses[0].observation") {
		t.Fatalf("violations = %s, want one naming the missing observation", fieldsOf(vs))
	}
}

func TestValidate_SupportedNeedsAnObservation(t *testing.T) {
	rec := grounded()
	rec.Hypotheses[1].Observation = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "hypotheses[1].observation") {
		t.Fatal("a supported hypothesis must cite what was seen")
	}
}

// TestValidate_CannotReproduceRefusesAFix — the "cannot reproduce" path is
// terminal by design. An investigation that cannot make the failure happen has
// no way to tell a fix from a coincidence.
func TestValidate_CannotReproduceRefusesAFix(t *testing.T) {
	rec := grounded()
	rec.Reproduction = Reproduction{Status: ReproNone, Attempts: []string{"ran the suite locally 20x", "re-ran the CI job twice"}}
	rec.SpikeIssue = "owner/app#400"
	vs := rec.Validate()
	if !strings.Contains(fieldsOf(vs), "fix") {
		t.Fatalf("violations = %s, want a refusal of the fix", fieldsOf(vs))
	}

	rec.Fix = nil
	if vs := rec.Validate(); len(vs) != 0 {
		t.Fatalf("a spike-and-stop record must validate, got %s", fieldsOf(vs))
	}
}

func TestValidate_CannotReproduceRequiresSpikeAndAttempts(t *testing.T) {
	rec := grounded()
	rec.Fix = nil
	rec.Reproduction = Reproduction{Status: ReproNone}
	got := fieldsOf(rec.Validate())
	for _, want := range []string{"reproduction.attempts", "spike_issue"} {
		if !strings.Contains(got, want) {
			t.Errorf("violations = %s, want one on %s", got, want)
		}
	}
}

// TestValidate_HistoryMustBeAnswered — a check that has never passed is not a
// regression, and treating it as one sends the investigation looking for a
// change that does not exist.
func TestValidate_HistoryMustBeAnswered(t *testing.T) {
	rec := grounded()
	rec.History = History{}
	if !strings.Contains(fieldsOf(rec.Validate()), "history.checked") {
		t.Fatal("the never-passed question must be answered explicitly")
	}
}

// TestValidate_NoTestRequiresSayingSo — admitting there is no test is allowed;
// implying coverage that does not exist is not.
func TestValidate_NoTestRequiresSayingSo(t *testing.T) {
	rec := grounded()
	rec.Fix.TestFailsWithoutFix = false
	rec.Fix.Test = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "fix.no_test_reason") {
		t.Fatal("a fix with no red-without-it test must say so plainly")
	}

	rec.Fix.NoTestReason = "the failure needs a booted emulator and a live stack; no harness reproduces it in unit scope"
	if vs := rec.Validate(); len(vs) != 0 {
		t.Fatalf("an honest no-test admission must validate, got %s", fieldsOf(vs))
	}
}

func TestValidate_ClaimedTestMustBeNamed(t *testing.T) {
	rec := grounded()
	rec.Fix.Test = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "fix.test") {
		t.Fatal("claiming a test that fails without the fix requires naming it")
	}
}

func TestValidate_LandedFixNeedsATrackingIssue(t *testing.T) {
	rec := grounded()
	rec.TrackingIssue = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "tracking_issue") {
		t.Fatal("a landed fix must be tracked somewhere other than a session transcript")
	}
}

func TestValidate_ReproductionNeedsEvidence(t *testing.T) {
	rec := grounded()
	rec.Reproduction.Evidence = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "reproduction.evidence") {
		t.Fatal("a claim of reproduction is not a reproduction")
	}
}

func TestValidate_RejectsUnknownEnums(t *testing.T) {
	rec := grounded()
	rec.Reproduction.Status = "sort of"
	rec.Hypotheses[0].Verdict = "maybe"
	got := fieldsOf(rec.Validate())
	for _, want := range []string{"reproduction.status", "hypotheses[0].verdict"} {
		if !strings.Contains(got, want) {
			t.Errorf("violations = %s, want one on %s", got, want)
		}
	}
}

func TestValidate_TargetNeedsARepo(t *testing.T) {
	rec := grounded()
	rec.Target.Repo = ""
	if !strings.Contains(fieldsOf(rec.Validate()), "target.repo") {
		t.Fatal("a check name means nothing without the repo it belongs to")
	}
}

func TestNewID(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if got := NewID("E2E Sweep / flutter", at); got != "e2e-sweep-flutter-20260901T120000Z" {
		t.Errorf("NewID = %q", got)
	}
	if got := NewID("", at); got != "triage-20260901T120000Z" {
		t.Errorf("NewID(empty) = %q", got)
	}
}

// TestWrite_PersistsAnInvalidRecord — a store that refused to keep a failing
// investigation would delete the record of what was tried, which is the part
// the next session needs most, and would push the author toward writing
// whatever the validator accepts rather than what happened.
func TestWrite_PersistsAnInvalidRecord(t *testing.T) {
	ws := t.TempDir()
	rec := grounded()
	rec.Hypotheses = []Hypothesis{rec.Hypotheses[1]}

	path, violations, err := Write(ws, rec)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("expected violations")
	}
	if path == "" {
		t.Fatal("the record must be written even when invalid")
	}
	back, err := Read(ws, rec.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if back.Reproduction.Evidence != rec.Reproduction.Evidence {
		t.Errorf("round-trip lost evidence: %q", back.Reproduction.Evidence)
	}
	ids, err := List(ws)
	if err != nil || len(ids) != 1 || ids[0] != rec.ID {
		t.Fatalf("List = %v (%v)", ids, err)
	}
}

func TestList_MissingDirIsEmpty(t *testing.T) {
	ids, err := List(t.TempDir())
	if err != nil || len(ids) != 0 {
		t.Fatalf("List = %v (%v), want empty and no error", ids, err)
	}
}
