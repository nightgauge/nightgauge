package doctor

import (
	"context"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
)

// Test adapters: the pre-registry tests assert on a (CheckItem, warning)
// view. asItem derives that view from a native check's findings so those
// tests keep asserting what each report names; the finding-level contract
// (codes, fingerprints, remedies) is asserted by the TestHygieneFindings and
// TestLearningFindings/TestAutomationFindings tests.
func asItem(fs []Finding, detail string) (CheckItem, string) {
	if len(fs) == 0 {
		return CheckItem{OK: true, Detail: detail}, ""
	}
	text := findingsText(fs)
	return CheckItem{OK: false, Detail: detail, Error: text}, text
}

func checkLeakedWorktrees(startDir string, now time.Time, door mergedPRDoorFactory) (CheckItem, string) {
	return asItem(worktreeLeakFindings(startDir, now, door))
}

func checkStrandedBranches(startDir string, door mergedPRDoorFactory) (CheckItem, string) {
	return asItem(strandedBranchFindings(startDir, door))
}

func checkPipelineStashes(startDir string, now time.Time) (CheckItem, string) {
	return asItem(pipelineStashFindings(startDir, now))
}

func checkPreservedWip(startDir string, now time.Time) (CheckItem, string) {
	return asItem(preservedWipFindings(startDir, now))
}

func checkServeLease(root string, now time.Time) (CheckItem, string) {
	return asItem(serveLeaseFindings(root, now))
}

func checkComplexityModel(root string) (CheckItem, string) {
	return asItem(complexityModelFindings(root))
}

func checkSurvivalBacklog(root string, now time.Time, window int) (CheckItem, string) {
	return asItem(survivalBacklogFindings(root, now, window))
}

func checkSurvivalCoverage(root string) (CheckItem, string) {
	return asItem(survivalCoverageFindings(root))
}

func checkCorpusCalibration(root string) (CheckItem, string) {
	return asItem(corpusCalibrationFindings(root))
}

func checkScheduledAutomations(ctx context.Context, probes map[cadence.EvidenceKind]cadenceProbe, scope cadence.Scope, declared []cadence.ConfigAutomation, now time.Time) (CheckItem, string) {
	return asItem(scheduledAutomationFindings(ctx, probes, scope, declared, nil, now))
}

func checkOrphanedProcesses(startDir string, now time.Time) (CheckItem, string) {
	return asItem(orphanedProcessFindings(startDir, now))
}
