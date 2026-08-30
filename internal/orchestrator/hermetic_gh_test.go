package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

// TestMain replaces the package's `gh` seam for the whole test binary before a
// single test runs.
//
// #492: an instrumented PATH shim measured 21 real `gh` subprocess calls per
// run of this package — every one an `issue view <n> --repo … --json state`
// from issueClosedOnForge, reached by tests that never installed
// stubReconcileGh. The suite's result therefore depended on `gh` being
// installed, being authenticated, and on GitHub being reachable and not rate
// limiting; ~12s of a ~42s -race run was real network round-trips against
// fixture repositories (nightgauge/test, acme/widget) that do not exist.
//
// The seam is closed here rather than by adding a stub to each of those tests
// because a per-test fix pins nothing: the next test to reach the reconcile
// paths without a stub silently goes back to the network. Overriding the
// default for the whole binary makes hermeticity a property of the package
// instead of a habit of its authors.
//
// It refuses rather than answering. A default fixture response would convert
// #492 into #474: an unstubbed test would keep passing while exercising a path
// nobody declared, and the reconcile arms are exactly the code where a wrong
// "the issue is closed on the forge" answer silently changes a run's terminal
// board status. A test that reaches gh must say what the forge answered.
func TestMain(m *testing.M) {
	reconcileExecGh = refuseUnstubbedGh
	finalizeDueSurvivalRecords = refuseUnstubbedSurvivalSweep
	code := m.Run()
	if calls := unstubbedGhCalls(); len(calls) > 0 {
		fmt.Fprintf(os.Stderr,
			"\nFAIL: %d unstubbed gh call(s) reached the reconcile paths:\n  gh %s\n\n"+
				"internal/orchestrator's test binary never shells out to gh (#492). A test that "+
				"reaches a reconcile path must declare what the forge answers:\n"+
				"  stubReconcileGh(t, func(ctx context.Context, args ...string) ([]byte, error) { ... })\n"+
				"or, when the point is that the forge cannot be reached and the path must fail closed:\n"+
				"  stubReconcileGhUnreachable(t)\n",
			len(calls), strings.Join(calls, "\n  gh "))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// unstubbedGh records every argv that reached the refusing default. A ledger
// drained at exit, rather than a panic at the call site: a panic aborts the
// whole binary on the first offender, which hides every other one and makes the
// suite unusable while they are being fixed. The run still fails — loudly, and
// naming every offending invocation — which is the property #492 asks for.
var unstubbedGh struct {
	mu    sync.Mutex
	calls []string
}

func unstubbedGhCalls() []string {
	unstubbedGh.mu.Lock()
	defer unstubbedGh.mu.Unlock()
	return append([]string(nil), unstubbedGh.calls...)
}

// refuseUnstubbedGh is the package's default `gh` implementation under test: it
// never execs anything, records the attempt, and returns an error so the caller
// takes its ordinary "the forge could not be queried" path for the remainder of
// the test instead of dereferencing a nil result.
func refuseUnstubbedGh(_ context.Context, args ...string) ([]byte, error) {
	argv := strings.Join(args, " ")
	unstubbedGh.mu.Lock()
	unstubbedGh.calls = append(unstubbedGh.calls, argv)
	unstubbedGh.mu.Unlock()
	return nil, fmt.Errorf("refusing to exec `gh %s`: internal/orchestrator tests are hermetic (#492)", argv)
}

// stubReconcileGhUnreachable declares the forge unreachable for the duration of
// a test. This is the honest replacement for the pre-#492 status quo, where
// these tests reached the real `gh`, got an error back from a fixture
// repository that does not exist, and exercised the fail-closed path by
// accident. The behaviour under test is unchanged; what changes is that the
// test now says so, and gets the same answer on a CI runner with no ambient
// auth, on a laptop that is logged in, and under rate limiting.
func stubReconcileGhUnreachable(t *testing.T) {
	t.Helper()
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("forge unreachable (test stub): gh %s", strings.Join(args, " "))
	})
}

// refuseUnstubbedSurvivalSweep closes the second `gh` seam a PATH shim found in
// this package (#492): gh.FinalizeDueSurvivalRecords builds a SurvivalDetector
// that shells out to `gh api .../commits` once per DUE record.
//
// It runs the real sweep with a REFUSING detector rather than short-circuiting
// the whole function, because the detector is the only surface in that path
// that talks to GitHub. A sweep with nothing pending does zero GitHub work and
// must stay silent here — most callers in this suite are in exactly that state,
// and flagging them would make the ledger noise instead of signal. Only a test
// that seeds a DUE record, and therefore would really have reached the network,
// is recorded and forced to say what the forge answers.
func refuseUnstubbedSurvivalSweep(ctx context.Context, root string, now time.Time, windowDays int) (gh.FinalizeResult, error) {
	return gh.FinalizeDueSurvivalRecordsWith(ctx, root, refusingSurvivalDetector{}, now, windowDays)
}

type refusingSurvivalDetector struct{}

func (refusingSurvivalDetector) Observe(_ context.Context, rec survival.Record) (survival.Observation, error) {
	unstubbedGh.mu.Lock()
	unstubbedGh.calls = append(unstubbedGh.calls,
		fmt.Sprintf("api <survival detect for %s#%d>", rec.Repo, rec.IssueNumber))
	unstubbedGh.mu.Unlock()
	return survival.Observation{}, fmt.Errorf(
		"refusing to observe %s#%d: internal/orchestrator tests are hermetic (#492)", rec.Repo, rec.IssueNumber)
}

// stubSurvivalSweep installs a survival-sweep implementation for one test.
func stubSurvivalSweep(t *testing.T, fn func(context.Context, string, time.Time, int) (gh.FinalizeResult, error)) {
	t.Helper()
	prev := finalizeDueSurvivalRecords
	finalizeDueSurvivalRecords = fn
	t.Cleanup(func() { finalizeDueSurvivalRecords = prev })
}

// stubSurvivalSweepNoForge declares that a test drives the sweep for its
// PLACEMENT or PACING and never reads what the forge said, so the sweep does no
// GitHub work at all. That is the whole truth about these tests, and saying it
// out loud is what stops the next reader from assuming the sweep's verdicts are
// under test here.
func stubSurvivalSweepNoForge(t *testing.T) {
	t.Helper()
	stubSurvivalSweep(t, func(context.Context, string, time.Time, int) (gh.FinalizeResult, error) {
		return gh.FinalizeResult{}, nil
	})
}
