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
	return timedKind("feature-dev", func() (bool, string, []string, Kind) {
		ctxPath := contextFilePath(workspace, "dev", issueNumber)
		data, err := os.ReadFile(ctxPath)
		if err != nil {
			if os.IsNotExist(err) {
				return false, "dev context file missing", []string{
					fmt.Sprintf("expected %s", ctxPath),
				}, KindNoOp
			}
			return false, "failed to read dev context file", []string{err.Error()}, KindFail
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
			return false, "dev context is not valid JSON", []string{err.Error()}, KindFail
		}

		fileTouches := len(devCtx.FilesChanged.Created) +
			len(devCtx.FilesChanged.Modified) +
			len(devCtx.FilesChanged.Deleted)
		if fileTouches == 0 {
			// The dev skill said success but recorded zero file changes — no-op.
			return false, "dev context records zero file changes", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindNoOp
		}

		if devCtx.BuildVerification == nil {
			// The skill skipped its verification step entirely — the exact
			// gap the Claude-only Stop hook used to cover on one adapter (#55).
			return false, "dev context lacks build_verification — the dev completion contract requires the verification step (nightgauge build run)", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindFail
		}

		if devCtx.BuildVerification.Ran &&
			devCtx.BuildVerification.Status == "failed" {
			// Build failure is a real fault, not a no-op — work happened, it broke.
			return false, "dev context records build_verification.status=failed", []string{
				fmt.Sprintf("file: %s", ctxPath),
			}, KindFail
		}

		if devCtx.TestsStatus != nil && devCtx.TestsStatus.Failed != nil &&
			*devCtx.TestsStatus.Failed > 0 {
			return false, "dev context records failing tests", []string{
				fmt.Sprintf("file: %s", ctxPath),
				fmt.Sprintf("tests_status.failed=%d", *devCtx.TestsStatus.Failed),
			}, KindFail
		}

		return true, "dev context records file changes, a recorded build verification, and no failing tests", []string{
			fmt.Sprintf("created=%d modified=%d deleted=%d build=%s",
				len(devCtx.FilesChanged.Created),
				len(devCtx.FilesChanged.Modified),
				len(devCtx.FilesChanged.Deleted),
				devCtx.BuildVerification.Status),
		}, KindOK
	})
}
