package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// The out-of-scope-blocker card is the DECISION REQUEST half of #1147: the
// finding is durable and defers the issue at pickup on every dispatch, so this
// card is the only thing in front of an operator that can end the hold. These
// tests pin the two properties that make it that rather than a notification.

func TestOutOfScopeBlockerOffersARealRetraction(t *testing.T) {
	r := BuildOutOfScopeBlocker("octocat/acme", 42, "run-1", "feature-validate")

	primary := r.FindOption("cleared")
	if primary == nil {
		t.Fatal("the card must offer a clear option — a durable hold with no retraction is a " +
			"silent permanent stall, which is the dead end ADR-015 Invariant 3 forbids")
	}
	if primary.Verb != attention.VerbBlockedFindingClear {
		t.Errorf("primary option binds %q, want %q — a noop here would be a button that does "+
			"nothing while the issue keeps deferring", primary.Verb, attention.VerbBlockedFindingClear)
	}
	// EMPTY arg surface: both coordinates of the target come from the persisted
	// request's Context, so a resolving surface can name neither repo nor issue.
	if len(primary.Args) != 0 {
		t.Errorf("primary option carries %d args, want 0", len(primary.Args))
	}
	// The default must NOT be the clear: an expiry that silently lifts the hold
	// would defeat the whole point of recording it.
	if r.DefaultAction == "cleared" {
		t.Error("default_action must not be the clear — expiry would lift the hold nobody resolved")
	}
	if r.FindOption(r.DefaultAction) == nil {
		t.Errorf("default_action %q is not a declared option", r.DefaultAction)
	}
}

func TestOutOfScopeBlockerIsAnEventNotAStandingCondition(t *testing.T) {
	r := BuildOutOfScopeBlocker("octocat/acme", 42, "run-1", "feature-validate")

	// Standing inherits ADR-015 §M suppression and needs a scan to auto-resolve
	// against. The raise verb has none, so the operator's first resolution would
	// silence the key forever while the finding it retracts stayed on disk.
	if r.Standing {
		t.Error("the card must not be Standing — nothing re-observes this condition")
	}
	if want := "out-of-scope-blocker:octocat/acme#42"; r.IdempotencyKey != want {
		t.Errorf("idempotency key = %q, want %q", r.IdempotencyKey, want)
	}
	if r.Context.Repo != "octocat/acme" || r.Context.Issue != 42 || r.Context.RunID != "run-1" {
		t.Errorf("context does not identify the run: %+v", r.Context)
	}
}

// The body has to tell an operator what NOT to expect, or the missing
// blockedBy edges read as a bug rather than the decision #1147 made.
func TestOutOfScopeBlockerBodyStatesThatNoEdgesWereCreated(t *testing.T) {
	r := BuildOutOfScopeBlocker("octocat/acme", 42, "run-1", "feature-validate")

	for _, want := range []string{"blockedBy", "add-blocked-by", BlockedFindingsDirName} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("body does not mention %q:\n%s", want, r.Body)
		}
	}
}

// The path is spelled out once on each side, and the DIRECTORY is what keeps
// runstate.ArchiveRun from sweeping the finding into history/<runID>/ at the
// end of the very run that wrote it.
func TestBlockedFindingPathIsUnderASubdirectoryOfPipeline(t *testing.T) {
	got := BlockedFindingPath("/repo", 42)
	want := filepath.Join("/repo", ".nightgauge", "pipeline", "blocked-findings", "42.json")
	if got != want {
		t.Errorf("BlockedFindingPath = %q, want %q", got, want)
	}
	if filepath.Base(filepath.Dir(got)) == "pipeline" {
		t.Error("the finding must NOT sit flat under .nightgauge/pipeline/ — ArchiveRun moves " +
			"every *-<issue>.json there into history/<runID>/ and skips directories")
	}
}
