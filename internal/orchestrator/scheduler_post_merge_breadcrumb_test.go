package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The merged PR the fixture's GraphQL server describes. The URL's owner/repo
// must be the run's TARGET repo, because verifyPRMerged derives its GitHub
// coordinates from the PR URL while checkEpicCompletion derives them from
// item.Repo — a cross-repo fixture that disagreed on those two would be
// testing the wrong seam.
const (
	breadcrumbTargetRepo = "nightgauge/other"
	breadcrumbPRURL      = "https://github.com/nightgauge/other/pull/931"
	breadcrumbMergeSHA   = "1f2e3d4c5b6a798877665544332211aabbccddee"
	breadcrumbMergedAt   = "2026-08-16T04:05:06Z"
	breadcrumbIssue      = 441
)

// mergedPRServer answers the single GraphQL query PRService.GetPR issues with a
// MERGED pull request that carries the #4133 ground-truth breadcrumb fields
// (mergedAt + mergeCommit.oid). Three separate calls land here on this path —
// verifyPRMerged's GetPR, EvaluatePostMerge's GetPRState, and its
// GetPRMergeInfo — and all three want the same answer, so one static handler is
// enough.
func mergedPRServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"data": {
				"repository": {
					"pullRequest": {
						"id": "PR_ID",
						"number": 931,
						"title": "",
						"body": "",
						"state": "MERGED",
						"headRefName": "",
						"baseRefName": "main",
						"url": "%s",
						"mergeable": "MERGEABLE",
						"mergeStateStatus": "CLEAN",
						"reviewDecision": "APPROVED",
						"isDraft": false,
						"additions": 0,
						"deletions": 0,
						"mergedAt": "%s",
						"mergeCommit": {"oid": "%s"},
						"labels": {"nodes": []},
						"commits": {"nodes": []}
					}
				}
			}
		}`, breadcrumbPRURL, breadcrumbMergedAt, breadcrumbMergeSHA)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// breadcrumbIssueSvc is the minimal issueGetter this path needs: a single
// non-epic, parentless issue whose close succeeds.
//
// Both properties are load-bearing rather than incidental. CloseIssue must
// SUCCEED because hooks.EvaluatePostMerge only sets SurvivalEligible when the
// issue actually closed, and the survival half of this test would otherwise
// pass vacuously (the package's shared mockIssueSvc returns "not implemented"
// from CloseIssue, which is why this fixture does not reuse it). And
// ParentIssueNumber must be 0 so EvaluatePostMerge returns at "no_parent"
// without an epic auto-close round trip.
type breadcrumbIssueSvc struct{ issue *types.Issue }

func (m *breadcrumbIssueSvc) GetIssue(_ context.Context, _, _ string, _ int) (*types.Issue, error) {
	return m.issue, nil
}

func (m *breadcrumbIssueSvc) GetIssuesByNumbers(_ context.Context, _, _ string, _ []int) (map[int]*types.Issue, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *breadcrumbIssueSvc) GetEpicProgress(_ context.Context, _ string) (*types.EpicProgress, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *breadcrumbIssueSvc) GetEpicProgressByNumber(_ context.Context, _, _ string, _ int) (*types.EpicProgress, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *breadcrumbIssueSvc) CloseIssue(_ context.Context, _ string) error { return nil }

func (m *breadcrumbIssueSvc) RemoveBlockedBy(_ context.Context, _, _ forge.IssueRef) error {
	return nil
}

// newBreadcrumbScheduler builds a scheduler whose LAUNCH root and whose run
// TARGET root are two different directories — the only shape in which a
// launch-rooted write is distinguishable from a run-rooted one.
func newBreadcrumbScheduler(t *testing.T, launchRoot, targetRoot string) *Scheduler {
	t.Helper()
	srv := mergedPRServer(t)
	s := &Scheduler{
		client:        gh.NewClientWithURL("test-token", srv.URL),
		issueSvc:      &breadcrumbIssueSvc{issue: &types.Issue{Number: breadcrumbIssue, NodeID: "I_441", State: "CLOSED"}},
		execMgr:       execution.NewManager(launchRoot, nil),
		workspaceRoot: launchRoot,
	}
	// The #229 seam the IPC server wires from ClientResolver.RepoPath. Only the
	// run's own repo resolves; anything else falls through to the execution
	// manager's launch root, so a misrouted lookup surfaces as a launch-rooted
	// write rather than as a silent pass.
	s.WithRepoPathResolver(func(repo string) string {
		if repo == breadcrumbTargetRepo {
			return targetRoot
		}
		return ""
	})
	return s
}

// TestVerifyPRMergeForStage_BreadcrumbLandsInTheRunsTargetRepo is the #441
// regression, and it pins a ROOT rather than a payload.
//
// The post-merge ground-truth breadcrumb (#4133) is a
// runtime-{issue}-{runId}.json write like every other Persist in runPipeline,
// but it was the one site that wrote through the scheduler field
// s.workspaceRoot — the LAUNCH root — instead of s.runRoot(item.Repo), the
// run's TARGET repo (#229). In a single-repo workspace those two are the same
// string, so the whole package was blind to the difference.
//
// The consequence is a misfiled identity, not merely a misplaced file. Since
// #410 state.ActiveIssuesFromSnapshots treats each repo root's own
// .nightgauge/pipeline/ as THAT root's in-flight source, and infers the owning
// repo from where the snapshot sits. A cross-repo run's breadcrumb landing in
// the launch root therefore gives the launch repo a phantom in-flight issue and
// leaves the target repo's scan one real run short — the same
// cross-contamination class as the history-record identity work
// (docs/GO_BINARY.md § Pipeline Repair History).
//
// verifyPRMergeForStage is exercised directly rather than through runPipeline
// on purpose: the breadcrumb is written on the pr-merge tail, and runPipeline's
// terminal tail removes the run's snapshot before it returns, so a
// post-run scan of either root would find nothing and every assertion here
// would be vacuous.
func TestVerifyPRMergeForStage_BreadcrumbLandsInTheRunsTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()

	s := newBreadcrumbScheduler(t, launchRoot, targetRoot)
	runtime := state.NewRuntimeState(breadcrumbTargetRepo, breadcrumbIssue, "item-441", testRunID())
	runtime.SetPrUrl(breadcrumbPRURL)
	item := types.BoardItem{Number: breadcrumbIssue, Repo: breadcrumbTargetRepo, ID: "item-441"}

	if failed := s.verifyPRMergeForStage(context.Background(), item, runtime, ""); failed {
		t.Fatalf("verifyPRMergeForStage reported a merge failure for a MERGED PR; the fixture is wrong, "+
			"not the assertion (stage error: %q)", runtime.Snapshot().StageErrors)
	}

	targetStateDir := filepath.Join(targetRoot, ".nightgauge", "pipeline")
	launchStateDir := filepath.Join(launchRoot, ".nightgauge", "pipeline")

	inTarget, err := state.FindPersistedStatesForIssue(targetStateDir, breadcrumbIssue)
	if err != nil {
		t.Fatalf("scan target state dir %s: %v", targetStateDir, err)
	}
	inLaunch, err := state.FindPersistedStatesForIssue(launchStateDir, breadcrumbIssue)
	if err != nil {
		t.Fatalf("scan launch state dir %s: %v", launchStateDir, err)
	}

	// NEGATIVE FIRST, deliberately. The positive arm has to Fatal (it indexes
	// inTarget[0]), so asserting it first would mask this one on exactly the
	// run that matters — the unfixed source, where BOTH are wrong. A misfiled
	// breadcrumb is read by ActiveIssuesFromSnapshots as an in-flight run
	// BELONGING TO the launch repo, which is a phantom.
	if len(inLaunch) != 0 {
		t.Errorf("the scheduler's LAUNCH root %s holds %d snapshots for #%d, want 0 — a cross-repo run's "+
			"breadcrumb filed here makes the launch repo's ActiveIssuesFromSnapshots scan report a phantom "+
			"in-flight issue while the target repo's scan misses a real one (#441 over #410)",
			launchStateDir, len(inLaunch), breadcrumbIssue)
	}

	// POSITIVE: the run's TARGET repo holds the breadcrumb, and it carries the
	// merge ground truth rather than merely existing.
	if len(inTarget) != 1 {
		t.Fatalf("the run's TARGET repo %s holds %d snapshots for #%d, want exactly 1 — the post-merge "+
			"breadcrumb must persist through s.runRoot(item.Repo) like every other Persist in runPipeline",
			targetStateDir, len(inTarget), breadcrumbIssue)
	}
	if got := inTarget[0].MergedCommitSha; got != breadcrumbMergeSHA {
		t.Errorf("target-rooted snapshot mergedCommitSha = %q, want %q — the file is in the right place but "+
			"does not carry the merge ground truth", got, breadcrumbMergeSHA)
	}
	if got := inTarget[0].MergedAt; got != breadcrumbMergedAt {
		t.Errorf("target-rooted snapshot mergedAt = %q, want %q", got, breadcrumbMergedAt)
	}
}

// TestVerifyPRMergeForStage_SurvivalRecordStaysAtTheLaunchRoot pins the
// deliberate ASYMMETRY that sits four lines below the breadcrumb, so a future
// reader who finds #441 does not "finish the job" by moving the survival writer
// too.
//
// The survival journal is not a per-run snapshot. It is one append-only file at
// <root>/survival.StoreRelPath whose records are self-describing — each carries
// its own Repo + Number, and gh.SurvivalDetector.Observe resolves the repo to
// query from rec.Repo, never from the store's root. Both readers are
// launch-root global with no per-repo scan to pair with:
// AutonomousScheduler.sweepSurvivalRecords opens
// survival.NewStore(as.workspaceRoot) and feeds
// gh.NewOutcomeService(as.workspaceRoot). Run-rooting only the writer would put
// a cross-repo run's record in a file nothing ever opens, and the sweep would
// age it out to "unobserved" — strictly worse than the bug #441 fixes.
//
// So the two writes SPLIT here by design, and this test is what makes that
// design a fact about the code rather than a claim in a comment.
func TestVerifyPRMergeForStage_SurvivalRecordStaysAtTheLaunchRoot(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()

	s := newBreadcrumbScheduler(t, launchRoot, targetRoot)
	runtime := state.NewRuntimeState(breadcrumbTargetRepo, breadcrumbIssue, "item-441", testRunID())
	runtime.SetPrUrl(breadcrumbPRURL)
	item := types.BoardItem{Number: breadcrumbIssue, Repo: breadcrumbTargetRepo, ID: "item-441"}

	if failed := s.verifyPRMergeForStage(context.Background(), item, runtime, ""); failed {
		t.Fatalf("verifyPRMergeForStage reported a merge failure for a MERGED PR; the fixture is wrong, "+
			"not the assertion (stage error: %q)", runtime.Snapshot().StageErrors)
	}

	// The journal the launch-root-global sweep will actually open.
	records, err := survival.NewStore(launchRoot).Load()
	if err != nil {
		t.Fatalf("load survival journal at launch root %s: %v", launchRoot, err)
	}
	if len(records) != 1 {
		t.Fatalf("launch root %s holds %d survival records, want exactly 1 — sweepSurvivalRecords opens "+
			"survival.NewStore(as.workspaceRoot), so a record written anywhere else is unreadable and ages "+
			"out to \"unobserved\"", launchRoot, len(records))
	}
	if records[0].Repo != breadcrumbTargetRepo {
		t.Errorf("survival record repo = %q, want %q — the record, not its location, is what carries repo "+
			"identity here", records[0].Repo, breadcrumbTargetRepo)
	}
	if records[0].MergeCommitSHA != breadcrumbMergeSHA {
		t.Errorf("survival record merge SHA = %q, want %q", records[0].MergeCommitSHA, breadcrumbMergeSHA)
	}

	// And nothing was ALSO written under the target root: one writer, one
	// journal. A second copy would double-count the merge in calibration.
	if _, statErr := os.Stat(filepath.Join(targetRoot, survival.StoreRelPath)); !os.IsNotExist(statErr) {
		t.Errorf("a survival journal exists under the run's target root %s (stat err: %v) — the survival "+
			"writer must stay launch-rooted to agree with its launch-rooted readers",
			targetRoot, statErr)
	}
}
