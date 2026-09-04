package stages

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// ── Cross-run client isolation (#1396) ────────────────────────────────────
//
// The scheduler holds ONE DeterministicRunner (`s.prMergeRunner`, wired once in
// newScheduler) and calls Run on it for every pipeline run. Any per-call write
// to a field on that instance is therefore shared mutable state across
// concurrent runs. The workdir-bound gh client used to be exactly that: Run
// assigned `r.gh = wb.withWorkdir(workdir)`, so a second run overwrote the
// first run's client mid-flight and the first run finished its merge through
// the second run's forge client — a cross-repo write in a workspace where the
// router binds a different project per repo.
//
// Two guards, because they fail for different reasons and only one of them is
// reliable:
//
//   - TestDeterministicRunner_ConcurrentRunsDoNotBleedClients is DETERMINISTIC.
//     It sequences the interleaving explicitly with channels, so it goes red on
//     the unfixed tree on every run, with or without -race.
//   - TestDeterministicRunner_ConcurrentRunsAreRaceFree is the -race guard the
//     issue asks for. It depends on the detector observing the unsynchronised
//     write, which is scheduling-dependent, so it is the weaker of the two and
//     exists to catch field writes the deterministic test does not model.

// boundProbe is a workdir-bound ghClient, shaped like the real execGhClient:
// withWorkdir returns a NEW client bound to that directory. Every View it
// serves records which bound directory served it, so a bleed is observable as
// a run's PR number being answered by another run's workdir.
type boundProbe struct {
	dir string
	log *sync.Map // prNumber -> the bound workdir that served it
}

func (b *boundProbe) withWorkdir(dir string) ghClient {
	return &boundProbe{dir: dir, log: b.log}
}

func (b *boundProbe) View(_ context.Context, prNumber int) (PRViewSnapshot, error) {
	if b.log != nil {
		b.log.Store(prNumber, b.dir)
	}
	return PRViewSnapshot{State: "MERGED", HeadRefName: "head"}, nil
}

func (b *boundProbe) Merge(_ context.Context, _ int) error { return nil }

// newConcurrencyRunner builds a runner that never sleeps and whose pr-context
// read is the injection point the tests use to control interleaving.
func newConcurrencyRunner(client ghClient) *DeterministicRunner {
	r := NewDeterministicRunnerWithClient(client)
	r.pollInterval = 0
	r.ciPollInterval = 0
	r.knowledgeConformance = nil
	return r
}

// TestDeterministicRunner_ConcurrentRunsDoNotBleedClients pins the exact
// failure #1396 describes, without depending on the race detector or on
// scheduling luck.
//
// The interleaving is forced: run A enters Run and parks after the point where
// the client is resolved; run B then resolves its own client; A resumes and
// issues its gh calls. On a runner that stores the resolved client on the
// shared instance, A's calls go out through B's client and the assertion below
// reports A's PR as having been served from B's workdir.
func TestDeterministicRunner_ConcurrentRunsDoNotBleedClients(t *testing.T) {
	var served sync.Map

	r := newConcurrencyRunner(&boundProbe{log: &served})

	aResolved := make(chan struct{})
	bResolved := make(chan struct{})

	// prContextRead runs AFTER the client is resolved and BEFORE any gh call,
	// which makes it the precise seam for forcing the overlap.
	r.prContextRead = func(_ string, issue int) (int, error) {
		if issue == 1 {
			close(aResolved)
			<-bResolved // hold A until B has resolved its own client
		} else {
			<-aResolved
			close(bResolved)
		}
		return issue, nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = r.Run(context.Background(), 1, "o/r", "/wd/A") }()
	go func() { defer wg.Done(); _, _ = r.Run(context.Background(), 2, "o/r", "/wd/B") }()
	wg.Wait()

	for _, tc := range []struct {
		pr   int
		want string
	}{
		{pr: 1, want: "/wd/A"},
		{pr: 2, want: "/wd/B"},
	} {
		got, ok := served.Load(tc.pr)
		if !ok {
			t.Fatalf("PR #%d was never served — the run did not reach a gh call", tc.pr)
		}
		if got != tc.want {
			t.Errorf("PR #%d was served by workdir %v, want %s — the runs shared one client",
				tc.pr, got, tc.want)
		}
	}
}

// TestDeterministicRunner_ConcurrentRunsAreRaceFree is the -race guard: many
// runs through one shared runner, each with its own workdir. On the unfixed
// tree the per-call assignment to the shared field races against every other
// run's read of it.
//
// Meaningful only under `go test -race`; without it the detector is off and the
// test degrades to a smoke test that the runner is concurrency-safe enough not
// to panic.
func TestDeterministicRunner_ConcurrentRunsAreRaceFree(t *testing.T) {
	const runs = 16

	var served sync.Map
	r := newConcurrencyRunner(&boundProbe{log: &served})
	r.prContextRead = func(_ string, issue int) (int, error) { return issue, nil }

	// Release every goroutine at once so the Run bodies genuinely overlap.
	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := 1; i <= runs; i++ {
		wg.Add(1)
		go func(issue int) {
			defer wg.Done()
			<-start
			_, _ = r.Run(context.Background(), issue, "o/r", fmt.Sprintf("/wd/%d", issue))
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 1; i <= runs; i++ {
		want := fmt.Sprintf("/wd/%d", i)
		got, ok := served.Load(i)
		if !ok {
			t.Fatalf("PR #%d was never served", i)
		}
		if got != want {
			t.Errorf("PR #%d was served by workdir %v, want %s", i, got, want)
		}
	}
}

// ── pr-create: the same instance is shared, and must stay stateless ───────
//
// `s.prCreateRunner` is wired once (scheduler.go, newScheduler) exactly like
// prMergeRunner, and the same instance is handed to the recovery registry. It
// does NOT have prMergeRunner's defect today — its clients are injected at
// construction by NewDefaultPRCreateRunner and its per-run inputs (workdir,
// issue, repo) all travel as arguments.
//
// That is a property worth holding rather than a fact worth asserting in a
// commit message: this is the guard that fails if a future change moves a
// per-call value onto the receiver, which is precisely how prmerge acquired
// the bug this file's other tests pin.

// concurrentPRClient is a thread-safe prCreateClient. Every CreatePR is
// answered with a PR number derived from the head branch, so a caller can tell
// which run's request it was.
type concurrentPRClient struct {
	mu    sync.Mutex
	heads []string
}

func (c *concurrentPRClient) GetRepoID(_ context.Context, _, _ string) (string, error) {
	return "REPO_ID", nil
}

func (c *concurrentPRClient) CreatePR(_ context.Context, _, _, _, head, _ string) (*CreatedPR, error) {
	c.mu.Lock()
	c.heads = append(c.heads, head)
	c.mu.Unlock()
	return &CreatedPR{Number: 1, URL: "https://example.invalid/pull/1", NodeID: "PR_1"}, nil
}

func (c *concurrentPRClient) ListOpenPRsForBranch(_ context.Context, _, _, _ string) ([]CreatedPR, error) {
	return nil, nil
}

// concurrentGit records which workdir each branch was pushed from, which is
// the pr-create analogue of prmerge's "which client served this PR".
type concurrentGit struct {
	pushedFrom *sync.Map // branch -> workdir it was pushed from
}

func (g *concurrentGit) PushBranch(_ context.Context, workdir, branch string) error {
	g.pushedFrom.Store(branch, workdir)
	return nil
}

func (g *concurrentGit) RemoteBranchExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (g *concurrentGit) CurrentBranch(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (g *concurrentGit) WorkingTreeStatus(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (g *concurrentGit) CommitsAhead(_ context.Context, _, _ string) (int, error) {
	return 1, nil
}

func (g *concurrentGit) CommitAll(_ context.Context, _, _ string, _ []string) (string, error) {
	return "", ErrNothingStaged
}

// TestDeterministicPRCreateRunner_ConcurrentRunsAreIsolated drives the shared
// pr-create runner from many goroutines at once, each with its own issue,
// branch and workdir, and asserts every branch was pushed from its own
// workdir. Run under -race it also catches an unsynchronised receiver write.
func TestDeterministicPRCreateRunner_ConcurrentRunsAreIsolated(t *testing.T) {
	const runs = 16

	var pushedFrom sync.Map

	r := NewDeterministicPRCreateRunner()
	r.prClient = &concurrentPRClient{}
	r.git = &concurrentGit{pushedFrom: &pushedFrom}
	// Each run reads a snapshot carrying its OWN branch, so the recorded
	// (branch -> workdir) pair identifies the run that produced it.
	r.readContext = func(_ string, issue int) (PRCreateSnapshot, error) {
		snap := richSnap()
		snap.IssueNumber = issue
		snap.Branch = fmt.Sprintf("feat/%d-x", issue)
		return snap, nil
	}
	r.writeContext = func(_ string, _ prContextPayload) error { return nil }

	start := make(chan struct{})

	var wg sync.WaitGroup
	for i := 1; i <= runs; i++ {
		wg.Add(1)
		go func(issue int) {
			defer wg.Done()
			<-start
			_, _ = r.Run(context.Background(), issue, "owner/repo", fmt.Sprintf("/wd/%d", issue))
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 1; i <= runs; i++ {
		branch := fmt.Sprintf("feat/%d-x", i)
		want := fmt.Sprintf("/wd/%d", i)
		got, ok := pushedFrom.Load(branch)
		if !ok {
			t.Fatalf("branch %s was never pushed — the run did not reach the push", branch)
		}
		if got != want {
			t.Errorf("branch %s was pushed from workdir %v, want %s — a per-run value is living on the shared runner",
				branch, got, want)
		}
	}
}
