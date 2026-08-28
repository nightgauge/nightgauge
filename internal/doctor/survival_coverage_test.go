package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

func mergedRun(issue int) learning.Outcome {
	return learning.Outcome{
		IssueNumber: issue,
		Repo:        "o/r",
		Success:     true,
		CompletedAt: time.Now(),
	}
}

// TestSurvivalCoverage_ReportsAJournalThatWasNeverWritten is the finding #1019
// asks for, and it asserts the OTHER arm's verdict alongside it on purpose.
//
// checkSurvivalBacklog returns OK for this exact workspace, correctly: it reads
// PENDING records and there are none. That clean bill is what made a dead
// writer invisible across a 113-run history. The two arms must be visibly
// distinguishable, or the second one is decoration.
func TestSurvivalCoverage_ReportsAJournalThatWasNeverWritten(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 12; i++ {
		rows = append(rows, mergedRun(i))
	}
	seedCorpus(t, root, rows...)
	// No survival records at all — the state the extension merge path produced.

	item, warning := checkSurvivalCoverage(root)
	if item.OK {
		t.Error("12 recorded runs and zero survival records reported OK — this is the exact " +
			"state the mandated dogfood path left the journal in")
	}
	if !strings.Contains(warning, "survival-capture-never-fired") {
		t.Errorf("warning lacks the stable identifier: %q", warning)
	}
	if !strings.Contains(warning, "12") {
		t.Errorf("warning does not name the run count that makes this a defect: %q", warning)
	}

	// The discriminating assertion: the pre-existing arm is green here.
	backlog, backlogWarning := checkSurvivalBacklog(root, time.Now(), survival.DefaultWindowDays)
	if !backlog.OK || backlogWarning != "" {
		t.Errorf("checkSurvivalBacklog should report OK for an EMPTY store — if it does not, "+
			"the two arms are not measuring different things. got OK=%v warning=%q",
			backlog.OK, backlogWarning)
	}
}

// TestSurvivalCoverage_IsQuietOnAYoungWorkspace pins the floor. Without it the
// arm fires on every fresh clone, which is how an absence detector becomes
// noise that gets muted — and a muted detector detects nothing.
func TestSurvivalCoverage_IsQuietOnAYoungWorkspace(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 3; i++ {
		rows = append(rows, mergedRun(i))
	}
	seedCorpus(t, root, rows...)

	item, warning := checkSurvivalCoverage(root)
	if !item.OK {
		t.Errorf("3 runs is below the %d-run floor and must not produce a finding: %q",
			minMergedRunsForCoverageFinding, item.Error)
	}
	if warning != "" {
		t.Errorf("young workspace produced a warning: %q", warning)
	}
}

// TestSurvivalCoverage_IsQuietWhenCaptureIsWorking is the other half of the
// floor: once records exist the arm must go silent, whatever their verdicts.
func TestSurvivalCoverage_IsQuietWhenCaptureIsWorking(t *testing.T) {
	root := t.TempDir()
	var rows []learning.Outcome
	for i := 1; i <= 12; i++ {
		rows = append(rows, mergedRun(i))
	}
	seedCorpus(t, root, rows...)

	store := survival.NewStore(root)
	rec := survival.NewPending("o/r", 1, 100, "abc1234", time.Now().Format(time.RFC3339), "")
	if _, err := store.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}

	item, warning := checkSurvivalCoverage(root)
	if !item.OK {
		t.Errorf("capture is live (1 record) but the arm fired: %q", item.Error)
	}
	if warning != "" {
		t.Errorf("live capture produced a warning: %q", warning)
	}
}
