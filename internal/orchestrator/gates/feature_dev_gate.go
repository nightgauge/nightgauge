package gates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// FeatureDevGate verifies the post-conditions of feature-dev:
//
//  1. pipeline/dev-{N}.json exists and parses
//  2. files_changed records at least one created or modified file (a dev
//     stage that records zero file changes is a skill-no-op)
//  3. build_verification is RECORDED (#55): the dev completion contract
//     requires the skill to have run its verification step (`nightgauge
//     build run` — provider-neutral). A missing object means the skill
//     skipped verification entirely; status=="skipped" with the object
//     present is legitimate (repo with no build system, fast-track
//     docs-only change).
//  4. build_verification did not record status=="failed"
//  5. tests_status, when recorded, has no failing tests (#55)
//  6. GROUND TRUTH (#202): git agrees that the stage workspace actually
//     changed. Checks 1–5 all read the skill's report of itself, which #202
//     showed is not the same question — a stage can report its work
//     truthfully and still have done it where no later stage can see it.
//
// Check 6 fails open on every uncertainty (not a repo, no base ref resolves).
// A gate that runs after the money is spent must never accuse on an answer it
// could not compute.
//
// Checks 3 and 5 replaced the Claude-only `hooks: Stop:` completion gate
// that re-ran vitest+build in a Claude subagent — that hook silently never
// fired on any other adapter (spike #33 finding D2) and hardcoded this
// repo's npm workspace layout. The gate consumes the evidence the skill
// recorded on ANY adapter; feature-validate re-runs the suite for real
// (re-running here would double the cost).
type FeatureDevGate struct{}

// Name implements StageGate.
func (FeatureDevGate) Name() string { return "feature-dev" }

// Verify implements StageGate.
func (FeatureDevGate) Verify(_ context.Context, issueNumber int, workspace string) GateResult {
	return timedFull("feature-dev", func() (bool, string, []string, Kind, string, []string, int) {
		ctxPath := contextFilePath(workspace, "dev", issueNumber)
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				// #223: ask git BEFORE calling this a no-op. A stage that did
				// the work and died before writing its handoff looks identical
				// here to a stage that did nothing, and the two demand opposite
				// recoveries.
				if v := devHandoffMissing(workspace, "dev context file missing", ctxPath); v.OK {
					return false, v.Reason, v.Evidence, v.Kind, v.TerminalKind, v.Files, v.FileCount
				}
				return false, "dev context file missing", []string{
					fmt.Sprintf("expected %s", ctxPath),
				}, KindNoOp, "", nil, 0
			}
			return false, "failed to read dev context file", []string{err.Error()}, KindFail, "", nil, 0
		}

		var devCtx struct {
			FilesChanged struct {
				Created  []string `json:"created"`
				Modified []string `json:"modified"`
				Deleted  []string `json:"deleted"`
			} `json:"files_changed"`
			BuildVerification *struct {
				Ran    bool   `json:"ran"`
				Status string `json:"status"`
			} `json:"build_verification"`
			TestsStatus *struct {
				Failed *int `json:"failed"`
			} `json:"tests_status"`
		}
		if err := json.Unmarshal(data, &devCtx); err != nil {
			reason, ev := describeDecodeFailure("dev context", err)
			return false, reason, ev, KindFail, TerminalKindValidationError, nil, 0
		}

		fileTouches := len(devCtx.FilesChanged.Created) +
			len(devCtx.FilesChanged.Modified) +
			len(devCtx.FilesChanged.Deleted)
		if fileTouches == 0 {
			// #223: same question as the missing-context path above. #221's
			// feature-dev wrote 206 insertions across 7 files plus a new
			// package, ended its turn on `echo waiting-for-notification`
			// without writing its handoff, and this check reported "zero file
			// changes" to an operator staring at a worktree full of them.
			if v := devHandoffMissing(workspace, "dev context records zero file changes", ctxPath); v.OK {
				return false, v.Reason, v.Evidence, v.Kind, v.TerminalKind, v.Files, v.FileCount
			}
			// The dev skill said success but recorded zero file changes — no-op.
			return false, "dev context records zero file changes", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindNoOp, "", nil, 0
		}

		if devCtx.BuildVerification == nil {
			// The skill skipped its verification step entirely — the exact
			// gap the Claude-only Stop hook used to cover on one adapter (#55).
			return false, "dev context lacks build_verification — the dev completion contract requires the verification step (nightgauge build run)", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindFail, "", nil, 0
		}

		if devCtx.BuildVerification.Ran &&
			devCtx.BuildVerification.Status == "failed" {
			// Build failure is a real fault, not a no-op — work happened, it broke.
			return false, "dev context records build_verification.status=failed", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindFail, "", nil, 0
		}

		if devCtx.TestsStatus != nil && devCtx.TestsStatus.Failed != nil &&
			*devCtx.TestsStatus.Failed > 0 {
			return false, "dev context records failing tests", []string{
				fmt.Sprintf("file: %s", ctxPath),
				fmt.Sprintf("tests_status.failed=%d", *devCtx.TestsStatus.Failed),
			}, KindFail, "", nil, 0
		}

		// Ground truth (#202). Every check above reads the skill's own report
		// of itself. A stage can describe its work perfectly honestly and
		// still have performed it somewhere no later stage will look, so the
		// last word belongs to git.
		//
		// #202: feature-dev spent 31 minutes on #173, wrote a dev context
		// truthfully listing five changed files, and passed this gate. The
		// files were in an agent-isolation worktree
		// (`.claude/worktrees/agent-<id>`); `.worktrees/issue-173` — the only
		// tree the pipeline reads — was clean, its branch tip an ancestor of
		// main. feature-validate then spent another $0.87 to discover that,
		// and reported it as its own `validation_failed`. Total $5.33 for zero
		// output, attributed to the wrong stage.
		declared := append(append(append([]string{}, devCtx.FilesChanged.Created...), devCtx.FilesChanged.Modified...), devCtx.FilesChanged.Deleted...)
		declared = withoutOwnHandoff(declared, issueNumber)
		work := inspectDevWork(workspace, declared)
		if work.Determined && !work.HasWork {
			evidence := []string{
				fmt.Sprintf("workspace: %s", workspace),
				fmt.Sprintf("dev context claims created=%d modified=%d deleted=%d",
					len(devCtx.FilesChanged.Created),
					len(devCtx.FilesChanged.Modified),
					len(devCtx.FilesChanged.Deleted)),
				"git: working tree clean and no commits on the branch its base lacks",
			}
			for _, s := range work.Stranded {
				evidence = append(evidence, "work stranded in sibling worktree: "+s)
			}
			// The `[dev-produced-no-changes]` marker mirrors the
			// `[no-changes-produced]` / `[adapter-auth-failed]` pattern: the
			// scheduler wraps this reason into a "premature turn end:" error
			// string, so without a stable marker the text-based classifiers
			// (Go ClassifyTerminalKind, the SDK health classifier) would bucket
			// it as premature_turn_end while the gate path reports
			// dev_produced_no_changes. Two classifiers disagreeing about one
			// failure is how a kind becomes untrustworthy in the dashboards.
			return false, "[dev-produced-no-changes] dev reported file changes but produced none in the stage workspace — the working tree is clean and the branch is level with its base", evidence, KindNoOp, TerminalKindDevProducedNoChanges, nil, 0
		}

		passEvidence := []string{
			fmt.Sprintf("created=%d modified=%d deleted=%d build=%s",
				len(devCtx.FilesChanged.Created),
				len(devCtx.FilesChanged.Modified),
				len(devCtx.FilesChanged.Deleted),
				devCtx.BuildVerification.Status),
		}
		if work.Mode == "bookkeeping" {
			passEvidence = append(passEvidence, fmt.Sprintf("deliverable=bookkeeping declared=%d confirmed=%d", work.DeclaredCount, work.ConfirmedCount))
		}
		return true, "dev context records file changes, a recorded build verification, and no failing tests", passEvidence, KindOK, "", nil, 0
	})
}
