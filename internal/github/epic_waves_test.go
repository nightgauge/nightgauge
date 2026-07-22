package github

import (
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestPlanWavesFromIssues_Empty(t *testing.T) {
	result := planWavesFromIssues(nil)
	if result.SubIssueCount != 0 {
		t.Errorf("expected SubIssueCount 0, got %d", result.SubIssueCount)
	}
	if len(result.Waves) != 0 {
		t.Errorf("expected no waves, got %d", len(result.Waves))
	}
}

func TestPlanWavesFromIssues_SingleNoDeps(t *testing.T) {
	issues := []types.Issue{
		{Number: 100, Title: "First issue"},
	}
	result := planWavesFromIssues(issues)
	if result.SubIssueCount != 1 {
		t.Errorf("expected SubIssueCount 1, got %d", result.SubIssueCount)
	}
	if len(result.Waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(result.Waves))
	}
	if result.Waves[0].WaveIndex != 0 {
		t.Errorf("expected wave index 0, got %d", result.Waves[0].WaveIndex)
	}
	if len(result.Waves[0].Issues) != 1 {
		t.Errorf("expected 1 issue in wave 0, got %d", len(result.Waves[0].Issues))
	}
}

func TestPlanWavesFromIssues_TwoIssuesWithDep(t *testing.T) {
	// Issue 200 is blocked by issue 100 → 100 should be wave 0, 200 should be wave 1
	issues := []types.Issue{
		{Number: 100, Title: "Blocker"},
		{Number: 200, Title: "Dependent", BlockedBy: []types.BlockingRef{{Number: 100}}},
	}
	result := planWavesFromIssues(issues)
	if result.SubIssueCount != 2 {
		t.Errorf("expected SubIssueCount 2, got %d", result.SubIssueCount)
	}
	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(result.Waves))
	}
	wave0 := result.Waves[0]
	wave1 := result.Waves[1]
	if len(wave0.Issues) != 1 || wave0.Issues[0].Number != 100 {
		t.Errorf("expected wave 0 to contain issue #100, got %v", wave0.Issues)
	}
	if len(wave1.Issues) != 1 || wave1.Issues[0].Number != 200 {
		t.Errorf("expected wave 1 to contain issue #200, got %v", wave1.Issues)
	}
}

func TestPlanWavesFromIssues_TwoIndependent(t *testing.T) {
	// Two issues with no dependencies — both should be in wave 0
	issues := []types.Issue{
		{Number: 100, Title: "First"},
		{Number: 101, Title: "Second"},
	}
	result := planWavesFromIssues(issues)
	if len(result.Waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(result.Waves))
	}
	if len(result.Waves[0].Issues) != 2 {
		t.Errorf("expected 2 issues in wave 0, got %d", len(result.Waves[0].Issues))
	}
}

func TestPlanWavesFromIssues_BlockerNotInList(t *testing.T) {
	// Issue 200 references blocker 999 which is not in the input list — should not panic
	issues := []types.Issue{
		{Number: 200, Title: "Has external blocker", BlockedBy: []types.BlockingRef{{Number: 999}}},
	}
	result := planWavesFromIssues(issues)
	if result.SubIssueCount != 1 {
		t.Errorf("expected SubIssueCount 1, got %d", result.SubIssueCount)
	}
	// External blocker is ignored; issue 200 has no internal deps → wave 0
	if len(result.Waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(result.Waves))
	}
	if result.Waves[0].Issues[0].Number != 200 {
		t.Errorf("expected issue #200 in wave 0, got %v", result.Waves[0].Issues)
	}
}

func TestPlanWavesFromIssues_FileOverlapSerializationParity(t *testing.T) {
	// After Files-from-Body population, two issues whose bodies reference the
	// same target file must be serialized into adjacent waves with the conflict
	// surfaced — identical to the shared teams.PlanWavesFromIssues behaviour.
	issues := []types.Issue{
		{Number: 143, Title: "Read-first view",
			Body: "Implements read-first mode in `lib/pages/journal_entry_page.dart`."},
		{Number: 144, Title: "Folders",
			Body: "Adds folders, touching lib/pages/journal_entry_page.dart."},
	}
	result := planWavesFromIssues(issues)

	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves after serialization, got %d", len(result.Waves))
	}
	if result.Waves[0].Issues[0].Number != 143 {
		t.Errorf("wave 0 should be #143, got %d", result.Waves[0].Issues[0].Number)
	}
	if result.Waves[1].Issues[0].Number != 144 {
		t.Errorf("wave 1 should be #144, got %d", result.Waves[1].Issues[0].Number)
	}
	if len(result.Conflicts) == 0 {
		t.Error("expected Conflicts populated with the shared file")
	}
}

func TestPlanWavesFromIssues_CycleDoesNotDeadlock(t *testing.T) {
	// Mutual blocking: A blocked by B, B blocked by A — CalculateWaves handles cycles
	issues := []types.Issue{
		{Number: 100, Title: "A", BlockedBy: []types.BlockingRef{{Number: 101}}},
		{Number: 101, Title: "B", BlockedBy: []types.BlockingRef{{Number: 100}}},
	}
	// Should not deadlock; CalculateWaves uses cycle-breaking fallback
	result := planWavesFromIssues(issues)
	if result.SubIssueCount != 2 {
		t.Errorf("expected SubIssueCount 2, got %d", result.SubIssueCount)
	}
	totalIssues := 0
	for _, w := range result.Waves {
		totalIssues += len(w.Issues)
	}
	if totalIssues != 2 {
		t.Errorf("expected 2 total issues across waves, got %d", totalIssues)
	}
}
