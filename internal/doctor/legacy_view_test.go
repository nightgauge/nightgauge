package doctor

import (
	"context"
	"strings"
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
	return asItem(pipelineStashFindings(startDir, now, nil))
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

func checkGitHubAPIBudget(root string, now time.Time) (CheckItem, string) {
	return asItem(apiBudgetFindings(root, now))
}

func checkLedgerDaemonCoverage(root string, now time.Time) (CheckItem, string) {
	return asItem(ledgerDaemonCoverageFindings(root, now))
}

func checkCIMachineCredentials(getenv func(string) string) (CheckItem, string) {
	return asItem(ciMachineCredentialFindings(getenv))
}

func checkSkillsRoot(startDir string) (CheckItem, string) {
	return asItem(skillsRootFindings(startDir))
}

func checkSkillsRootIn(roots []string) (CheckItem, string) {
	return asItem(skillsRootFindingsIn(roots))
}

func checkAIAdapterAvailable(probe adapterProbe) (CheckItem, string) {
	return asItem(aiAdapterFindings(probe))
}

// checkTrackedCredentials also carries the structured scan, which the
// pre-registry tests assert on.
func checkTrackedCredentials(dir string) (CheckItem, string) {
	fs, detail := trackedCredentialFindings(dir)
	item, warn := asItem(fs, detail)
	if len(fs) > 0 {
		item.Error += "; " + detail
	}
	item.Findings = scanTrackedCredentials(dir).findings
	return item, warn
}

// resultItem derives a (CheckItem) view of one check from a run's results: OK
// when the check ran and raised no blocker or warning. present is false when
// the check did not apply here.
func resultItemOK(res DoctorResult, id string) (CheckItem, bool) {
	for _, r := range res.Results {
		if r.ID != id {
			continue
		}
		if strings.HasPrefix(r.Detail, "not applicable") {
			return CheckItem{}, false
		}
		ok := r.Status != StatusSkipped && r.Status != StatusTimeout
		for _, f := range r.Findings {
			if f.Severity == SeverityBlocker || f.Severity == SeverityWarning {
				ok = false
			}
		}
		item := CheckItem{OK: ok, Detail: r.Detail}
		if len(r.Findings) > 0 {
			item.Error = findingsText(r.Findings)
		}
		return item, true
	}
	return CheckItem{}, false
}

func resultItem(res DoctorResult, id string) CheckItem {
	item, _ := resultItemOK(res, id)
	return item
}
