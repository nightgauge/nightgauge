package teams

import (
	"testing"

	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

func TestPlanWavesFromIssues_Empty(t *testing.T) {
	result := PlanWavesFromIssues(nil)
	if result.SubIssueCount != 0 {
		t.Errorf("SubIssueCount = %d, want 0", result.SubIssueCount)
	}
	if len(result.Waves) != 0 {
		t.Errorf("Waves = %d, want 0", len(result.Waves))
	}
}

func TestPlanWavesFromIssues_SingleNoDeps(t *testing.T) {
	issues := []pkgtypes.Issue{{Number: 100, Title: "First"}}
	result := PlanWavesFromIssues(issues)
	if result.SubIssueCount != 1 || len(result.Waves) != 1 {
		t.Fatalf("got SubIssueCount=%d Waves=%d, want 1/1", result.SubIssueCount, len(result.Waves))
	}
	if result.Waves[0].WaveIndex != 0 || len(result.Waves[0].Issues) != 1 {
		t.Errorf("wave 0 shape = %+v", result.Waves[0])
	}
}

func TestPlanWavesFromIssues_TwoIssuesWithDep(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Number: 100, Title: "Blocker"},
		{Number: 200, Title: "Dependent", BlockedBy: []pkgtypes.BlockingRef{{Number: 100}}},
	}
	result := PlanWavesFromIssues(issues)
	if len(result.Waves) != 2 {
		t.Fatalf("Waves = %d, want 2", len(result.Waves))
	}
	if result.Waves[0].Issues[0].Number != 100 {
		t.Errorf("wave 0 issue = %d, want 100", result.Waves[0].Issues[0].Number)
	}
	if result.Waves[1].Issues[0].Number != 200 {
		t.Errorf("wave 1 issue = %d, want 200", result.Waves[1].Issues[0].Number)
	}
}

func TestPlanWavesFromIssues_TwoIndependent(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Number: 100, Title: "First"},
		{Number: 101, Title: "Second"},
	}
	result := PlanWavesFromIssues(issues)
	if len(result.Waves) != 1 {
		t.Fatalf("Waves = %d, want 1", len(result.Waves))
	}
	if len(result.Waves[0].Issues) != 2 {
		t.Errorf("wave 0 size = %d, want 2", len(result.Waves[0].Issues))
	}
}

func TestPlanWavesFromIssues_BlockerNotInList(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Number: 200, Title: "External blocker", BlockedBy: []pkgtypes.BlockingRef{{Number: 999}}},
	}
	result := PlanWavesFromIssues(issues)
	if result.SubIssueCount != 1 || len(result.Waves) != 1 {
		t.Fatalf("got SubIssueCount=%d Waves=%d, want 1/1", result.SubIssueCount, len(result.Waves))
	}
	if result.Waves[0].Issues[0].Number != 200 {
		t.Errorf("wave 0 issue = %d, want 200", result.Waves[0].Issues[0].Number)
	}
}

func TestPlanWavesFromIssues_FiveIssueGraph(t *testing.T) {
	// Topology:
	//   1 → 2
	//   1 → 3
	//   2 → 4
	//   3 → 4
	//   4 → 5
	// Expected waves: [1] / [2, 3] / [4] / [5]
	issues := []pkgtypes.Issue{
		{Number: 1, Title: "Root"},
		{Number: 2, Title: "B", BlockedBy: []pkgtypes.BlockingRef{{Number: 1}}},
		{Number: 3, Title: "C", BlockedBy: []pkgtypes.BlockingRef{{Number: 1}}},
		{Number: 4, Title: "D", BlockedBy: []pkgtypes.BlockingRef{{Number: 2}, {Number: 3}}},
		{Number: 5, Title: "E", BlockedBy: []pkgtypes.BlockingRef{{Number: 4}}},
	}
	result := PlanWavesFromIssues(issues)
	if len(result.Waves) != 4 {
		t.Fatalf("Waves = %d, want 4", len(result.Waves))
	}
	if len(result.Waves[0].Issues) != 1 || result.Waves[0].Issues[0].Number != 1 {
		t.Errorf("wave 0 = %+v, want [1]", waveNumbers(result.Waves[0]))
	}
	if len(result.Waves[1].Issues) != 2 {
		t.Errorf("wave 1 size = %d, want 2", len(result.Waves[1].Issues))
	}
	if len(result.Waves[3].Issues) != 1 || result.Waves[3].Issues[0].Number != 5 {
		t.Errorf("wave 3 = %+v, want [5]", waveNumbers(result.Waves[3]))
	}
}

func TestPlanWavesFromIssues_FileOverlapSerialized(t *testing.T) {
	// Two issues with no explicit blockedBy but bodies referencing the same
	// target file must be serialized into adjacent waves with a populated
	// Conflicts list (the authoring-side AC for #4074).
	issues := []pkgtypes.Issue{
		{Number: 143, Title: "Read-first view",
			Body: "Implements read-first mode in `lib/pages/journal_entry_page.dart`."},
		{Number: 144, Title: "Folders",
			Body: "Adds folders, touching lib/pages/journal_entry_page.dart."},
	}
	result := PlanWavesFromIssues(issues)

	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves after serialization, got %d (%+v)", len(result.Waves), result.Waves)
	}
	if result.Waves[0].Issues[0].Number != 143 {
		t.Errorf("wave 0 should be #143 (earlier), got %d", result.Waves[0].Issues[0].Number)
	}
	if result.Waves[1].Issues[0].Number != 144 {
		t.Errorf("wave 1 should be #144 (later), got %d", result.Waves[1].Issues[0].Number)
	}
	if len(result.Conflicts) == 0 {
		t.Fatal("expected Conflicts to be populated with the shared file")
	}
	found := false
	for _, c := range result.Conflicts {
		if c.Path == "lib/pages/journal_entry_page.dart" && c.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error conflict for the shared file, got %+v", result.Conflicts)
	}

	// Files were extracted from bodies onto the SubIssues in each wave.
	if len(result.Waves[0].Issues[0].Files) == 0 {
		t.Error("expected SubIssue.Files to be populated from Issue.Body")
	}
}

func TestPlanWavesFromIssues_PreExistingBlockedByNotDuplicated(t *testing.T) {
	// When a shared-file pair already has an explicit blockedBy edge, the
	// serialization must respect it and not produce a redundant injected edge.
	issues := []pkgtypes.Issue{
		{Number: 143, Title: "Read-first view",
			Body: "Edits `lib/pages/journal_entry_page.dart`."},
		{Number: 144, Title: "Folders",
			Body:      "Also edits lib/pages/journal_entry_page.dart.",
			BlockedBy: []pkgtypes.BlockingRef{{Number: 143}}},
	}
	result := PlanWavesFromIssues(issues)

	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(result.Waves))
	}
	if result.Waves[0].Issues[0].Number != 143 || result.Waves[1].Issues[0].Number != 144 {
		t.Errorf("expected wave0=#143, wave1=#144; got %v / %v",
			waveNumbers(result.Waves[0]), waveNumbers(result.Waves[1]))
	}
	// The pair was already ordered, so SerializeFileOverlaps injects nothing.
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no injected conflicts for an already-ordered pair, got %+v", result.Conflicts)
	}
}

func TestPlanWavesFromIssues_DirectoryOverlapStaysParallel(t *testing.T) {
	// Different files in the same directory must remain parallel (single wave).
	issues := []pkgtypes.Issue{
		{Number: 1, Title: "A", Body: "Edits `lib/pages/a.dart`."},
		{Number: 2, Title: "B", Body: "Edits `lib/pages/b.dart`."},
	}
	result := PlanWavesFromIssues(issues)
	if len(result.Waves) != 1 || len(result.Waves[0].Issues) != 2 {
		t.Errorf("expected 1 wave of 2 issues for directory-only overlap, got %+v", result.Waves)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no error conflicts for directory-only overlap, got %+v", result.Conflicts)
	}
}

func waveNumbers(w WaveAssignment) []int {
	out := make([]int, len(w.Issues))
	for i, s := range w.Issues {
		out[i] = s.Number
	}
	return out
}

// ── #79: epic #71 reproduction — shared doc citation must not serialize ──

// TestPlanWavesFromIssues_SharedDocCitationStaysParallel reproduces the epic
// #71 failure: six sub-issues each citing the same spike doc were serialized
// into six sequential waves (13 phantom blockedBy edges) with zero real
// dependencies. Shared documentation references — linked OR bare-path — must
// never erase an epic's parallelism.
func TestPlanWavesFromIssues_SharedDocCitationStaysParallel(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Number: 72, Title: "eval axis",
			Body: "Add the axis. Context: [spike](docs/spikes/fable-5-behavior-porting.md) §4."},
		{Number: 73, Title: "fable budgets",
			Body: "Fix budgets. Evidence in docs/spikes/fable-5-behavior-porting.md §3."},
		{Number: 74, Title: "registry",
			Body: "Update registry. See docs/spikes/fable-5-behavior-porting.md §6."},
		{Number: 75, Title: "docs",
			Body: "Document it. Per docs/spikes/fable-5-behavior-porting.md §7."},
	}
	result := PlanWavesFromIssues(issues)

	if len(result.Waves) != 1 {
		t.Fatalf("expected 1 parallel wave (shared doc is a citation, not a target), got %d: %+v",
			len(result.Waves), result.Waves)
	}
	if got := len(result.Waves[0].Issues); got != 4 {
		t.Errorf("expected all 4 sub-issues in wave 0, got %d", got)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no conflicts for a shared doc citation, got %+v", result.Conflicts)
	}
}

// The #143/#144 defense must not regress (#79 AC): a genuine same-CODE-file
// write collision still serializes even alongside a shared doc citation.
func TestPlanWavesFromIssues_CodeCollisionStillSerializedWithDocCitation(t *testing.T) {
	issues := []pkgtypes.Issue{
		{Number: 143, Title: "Read-first view",
			Body: "Edit `lib/pages/journal_entry_page.dart`. See docs/spikes/shared.md."},
		{Number: 144, Title: "Folders",
			Body: "Edit `lib/pages/journal_entry_page.dart`. See docs/spikes/shared.md."},
	}
	result := PlanWavesFromIssues(issues)

	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves (real code collision), got %d", len(result.Waves))
	}
	for _, c := range result.Conflicts {
		if c.Path == "docs/spikes/shared.md" {
			t.Errorf("doc path must not appear as a conflict, got %+v", result.Conflicts)
		}
	}
}
