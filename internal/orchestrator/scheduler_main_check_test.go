package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// redMainReader answers every check-runs read with one concluded failure, so
// the post-merge observation is red on its first poll and the test never
// sleeps.
type redMainReader struct{ refs []string }

func (r *redMainReader) GetIndividualCheckRuns(_ context.Context, _, _, ref string) ([]forgetypes.CheckDetail, error) {
	r.refs = append(r.refs, ref)
	return []forgetypes.CheckDetail{
		{Name: "e2e", Status: "COMPLETED", Conclusion: "FAILURE", DetailsURL: "https://ci/e2e"},
		{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
	}, nil
}

func (r *redMainReader) GetRequiredCheckNames(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

// TestVerifyPRMergeForStage_ObservesMainAndRecordsTheVerdict is the scheduler
// half of #1249. Through the real hooks.EvaluatePostMerge against the fixture
// forge, a merge whose commit turns main red must leave three durable traces:
// the verdict on the run record, the verdict on the survival record, and a
// merge-commit-checks card in the Action Center.
func TestVerifyPRMergeForStage_ObservesMainAndRecordsTheVerdict(t *testing.T) {
	launchRoot := t.TempDir()
	targetRoot := t.TempDir()
	s := newBreadcrumbScheduler(t, launchRoot, targetRoot)
	reader := &redMainReader{}
	s.mainCheckReaderFn = func(*gh.Client) hooks.MainCheckReader { return reader }
	s.SetAttention(attention.New(t.TempDir()))

	runtime := state.NewRuntimeState(breadcrumbTargetRepo, breadcrumbIssue, "item-441", testRunID())
	runtime.SetPrUrl(breadcrumbPRURL)
	item := types.BoardItem{Number: breadcrumbIssue, Repo: breadcrumbTargetRepo, ID: "item-441"}

	if failed := s.verifyPRMergeForStage(context.Background(), item, runtime, ""); failed {
		t.Fatalf("verifyPRMergeForStage reported a merge failure for a MERGED PR (stage error: %q)", runtime.Snapshot().StageErrors)
	}

	if len(reader.refs) == 0 || reader.refs[0] != breadcrumbMergeSHA {
		t.Fatalf("check runs read for %v, want the merge commit %s — the pipeline never observed main", reader.refs, breadcrumbMergeSHA)
	}

	// Run record.
	snaps, err := state.FindPersistedStatesForIssue(filepath.Join(targetRoot, ".nightgauge", "pipeline"), breadcrumbIssue)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("target snapshots: %d (%v), want 1", len(snaps), err)
	}
	if snaps[0].MainCheckVerdict != string(hooks.MainChecksRed) {
		t.Errorf("persisted mainCheckVerdict = %q, want red", snaps[0].MainCheckVerdict)
	}
	if strings.Join(snaps[0].MainCheckFailing, ",") != "e2e" {
		t.Errorf("persisted mainCheckFailing = %v, want [e2e]", snaps[0].MainCheckFailing)
	}

	// Survival record.
	recs, err := survival.NewStore(launchRoot).Load()
	if err != nil || len(recs) != 1 {
		t.Fatalf("survival records at the launch root: %d (%v), want 1", len(recs), err)
	}
	if recs[0].MainCheckVerdict != string(hooks.MainChecksRed) || strings.Join(recs[0].MainCheckFailing, ",") != "e2e" {
		t.Errorf("survival record main check = %q %v, want red [e2e]", recs[0].MainCheckVerdict, recs[0].MainCheckFailing)
	}
	if recs[0].BaseRef != "main" {
		t.Errorf("survival record BaseRef = %q, want the PR's base from the breadcrumb", recs[0].BaseRef)
	}

	// Action Center.
	open, err := s.attention.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 || open[0].Producer != hooks.ProducerMergeCommitChecks {
		t.Fatalf("open cards = %+v, want exactly one %s card", open, hooks.ProducerMergeCommitChecks)
	}
	if open[0].Context.PR != 931 || open[0].Context.Repo != breadcrumbTargetRepo {
		t.Errorf("card context = %+v, want PR 931 on %s", open[0].Context, breadcrumbTargetRepo)
	}
}

// The reader reaches EvaluatePostMerge only through the seam, and only when
// there is a client to build it from. A hand-built Scheduler with no seam gets
// no reader (and so cannot reach api.github.com by omission); NewScheduler
// installs the real one.
func TestCheckEpicCompletion_WiresTheMainCheckReaderThroughTheSeam(t *testing.T) {
	var got hooks.PostMergeInput
	capture := func(_ context.Context, _ hooks.IssueFetcher, _ hooks.IssueCloser, _ hooks.EpicAutoCloser,
		_ hooks.PRVerifier, _ hooks.BoardSyncer, in hooks.PostMergeInput) hooks.PostMergeResult {
		got = in
		return hooks.PostMergeResult{}
	}
	reader := &redMainReader{}

	s := &Scheduler{
		client:              gh.NewClientWithURL("t", "http://127.0.0.1:0/graphql"),
		evaluatePostMergeFn: capture,
		mainCheckReaderFn:   func(*gh.Client) hooks.MainCheckReader { return reader },
	}
	s.checkEpicCompletion(context.Background(), types.BoardItem{Repo: "o/r", Number: 1}, 2)
	if got.MainChecks != reader {
		t.Errorf("MainChecks = %v, want the seam's reader", got.MainChecks)
	}
	if got.MainCheckWait.Timeout != hooks.DefaultMainCheckTimeout {
		t.Errorf("MainCheckWait.Timeout = %v, want the pipeline's default bounded budget %v", got.MainCheckWait.Timeout, hooks.DefaultMainCheckTimeout)
	}

	s = &Scheduler{evaluatePostMergeFn: capture, mainCheckReaderFn: func(*gh.Client) hooks.MainCheckReader { return reader }}
	s.checkEpicCompletion(context.Background(), types.BoardItem{Repo: "o/r", Number: 1}, 2)
	if got.MainChecks != nil {
		t.Errorf("no client: MainChecks = %v, want nil", got.MainChecks)
	}

	real := NewScheduler(nil, SchedulerConfig{WorkspaceRoot: t.TempDir()})
	if real.mainCheckReaderFn == nil {
		t.Fatal("NewScheduler left mainCheckReaderFn nil — the pipeline path would never observe main")
	}
	if _, ok := real.mainCheckReaderFn(gh.NewClientWithURL("t", "http://127.0.0.1:0/graphql")).(*gh.CIService); !ok {
		t.Error("NewScheduler's reader is not the GitHub CI service")
	}
}
