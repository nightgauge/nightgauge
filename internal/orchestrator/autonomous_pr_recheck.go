// autonomous_pr_recheck.go — re-read the PR of a run nobody could follow to
// its end (#2607).
//
// A run killed by a transient failure after pr-create (an API connection
// lost, a network outage, a stall during the pr-merge wait) leaves a PR the
// merge queue may merge while the daemon cannot reach the forge. Nothing read
// that PR again: the failure path recorded no PR number, the revert could not
// list open PRs and asked a human to move the issue by hand, and once the
// merge closed the issue it left the graph and its Failed entry was pruned.
// The post-merge path (validation marker, main-check verdict, board and epic
// rollups) never ran.
//
// PendingPRRechecks records such runs, and reconcilePRRechecks reads each one
// every scan until the forge answers. The next successful read is the
// connectivity signal; there is no separate probe.
//
//   - MERGED: run the same post-merge path a run that reached its merge stage
//     runs (Scheduler.runPostMerge), once, then record the issue completed and
//     force the epic-rollup sweep so every configured board reaches Done.
//   - OPEN, CLOSED, or no PR recorded: revertFailedIssueStatus decides from
//     the open-PR list. A transient kind on an open PR goes to Ready and the
//     re-dispatch resumes at pr-merge on that PR (#2554).
//   - Any read error: keep the entry for the next scan.

package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// PRRecheckItem is one failed run whose PR is still to be read (#2607).
type PRRecheckItem struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// PRNumber is the run's PR, from its runtime snapshot; 0 when the run had
	// none on record (the revert's open-PR lookup then decides).
	PRNumber int `json:"prNumber,omitempty"`
	// Kind is the run's terminal failure kind, handed to the revert so a
	// transient kind on an open PR still goes to Ready.
	Kind       string `json:"kind,omitempty"`
	RecordedAt string `json:"recordedAt"`
}

// recordTransientRunPR queues a recheck for a failed, unmerged run that ended
// on a transient kind with a PR on record. Such a run can die after pr-create
// while its PR sits in the merge queue, and the queue may merge it while the
// forge is unreachable; the recheck reads it once the forge answers, whatever
// the failure path's own revert managed to do.
func (as *AutonomousScheduler) recordTransientRunPR(repo string, issue int, runtime *state.RuntimeState, kind string) {
	if runtime == nil || !isTransientRevertKind(kind) {
		return
	}
	// The PR is bound to the run by its URL: one naming another repository is
	// not this issue's PR, and reading it MERGED must never close this issue.
	prOwner, prRepo, prNum, err := parsePRURL(runtime.Snapshot().PrUrl)
	if err != nil || prNum <= 0 {
		return
	}
	if !strings.EqualFold(prOwner+"/"+prRepo, repo) {
		log.Printf("autonomous: pr-recheck: %s#%d run PR %s/%s#%d is in another repository — not re-checking it",
			repo, issue, prOwner, prRepo, prNum)
		return
	}
	as.enqueuePRRecheck(repo, issue, prNum, kind)
}

// enqueuePRRecheck records a recheck for repo#issue and persists the state.
func (as *AutonomousScheduler) enqueuePRRecheck(repo string, issue, prNumber int, kind string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.enqueuePRRecheckLocked(repo, issue, prNumber, kind)
	as.persistStateLocked()
}

// enqueuePRRecheckLocked adds or updates the entry for repo#issue. One entry
// per issue: a later call keeps a known PR number over 0 and refreshes the
// kind. Caller must hold as.mu.
func (as *AutonomousScheduler) enqueuePRRecheckLocked(repo string, issue, prNumber int, kind string) {
	for i := range as.state.PendingPRRechecks {
		e := &as.state.PendingPRRechecks[i]
		if e.Repo != repo || e.Number != issue {
			continue
		}
		if prNumber > 0 {
			e.PRNumber = prNumber
		}
		if kind != "" {
			e.Kind = kind
		}
		return
	}
	as.state.PendingPRRechecks = append(as.state.PendingPRRechecks, PRRecheckItem{
		Repo:       repo,
		Number:     issue,
		PRNumber:   prNumber,
		Kind:       kind,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
	})
	log.Printf("autonomous: pr-recheck: queued %s#%d (PR #%d, kind %q) — will re-check once the forge answers",
		repo, issue, prNumber, kind)
}

// removePRRecheckLocked drops the entry for repo#issue and reports whether
// there was one. Caller must hold as.mu.
func (as *AutonomousScheduler) removePRRecheckLocked(repo string, issue int) bool {
	for i, e := range as.state.PendingPRRechecks {
		if e.Repo == repo && e.Number == issue {
			as.state.PendingPRRechecks = append(as.state.PendingPRRechecks[:i], as.state.PendingPRRechecks[i+1:]...)
			return true
		}
	}
	return false
}

// hasPRRecheckLocked reports whether repo#issue has a pending recheck. Caller
// must hold as.mu.
func (as *AutonomousScheduler) hasPRRecheckLocked(repo string, issue int) bool {
	for _, e := range as.state.PendingPRRechecks {
		if e.Repo == repo && e.Number == issue {
			return true
		}
	}
	return false
}

// startPRRecheckSweep runs reconcilePRRechecks as detached board work when
// there is something to recheck and no sweep is already running. The sweep's
// forge calls carry boardRecoveryTimeout, which must not hold the scan cycle.
func (as *AutonomousScheduler) startPRRecheckSweep() {
	as.mu.Lock()
	pending := len(as.state.PendingPRRechecks)
	as.mu.Unlock()
	if pending == 0 || !as.prRecheckInFlight.CompareAndSwap(false, true) {
		return
	}
	as.goTrackedBoardOp(func(ctx context.Context) {
		defer as.prRecheckInFlight.Store(false)
		as.reconcilePRRechecks(ctx)
	})
}

// reconcilePRRechecks reads every pending recheck once. An issue that is
// running again is skipped: its run follows its own PR, and a revert now
// would move a running issue's board row.
func (as *AutonomousScheduler) reconcilePRRechecks(ctx context.Context) {
	as.mu.Lock()
	pending := append([]PRRecheckItem(nil), as.state.PendingPRRechecks...)
	running := make(map[string]bool, len(as.state.Running))
	for _, r := range as.state.Running {
		running[fmt.Sprintf("%s#%d", r.Repo, r.Number)] = true
	}
	as.mu.Unlock()

	for _, item := range pending {
		if ctx.Err() != nil {
			return
		}
		if running[fmt.Sprintf("%s#%d", item.Repo, item.Number)] {
			continue
		}
		as.recheckPR(ctx, item)
	}
}

// recheckPR resolves one entry, or leaves it for the next scan when the
// forge did not answer.
func (as *AutonomousScheduler) recheckPR(ctx context.Context, item PRRecheckItem) {
	if item.PRNumber > 0 {
		owner, repoName := splitOwnerRepo(item.Repo)
		readCtx, cancel := context.WithTimeout(ctx, boardRecoveryTimeout)
		prState, err := as.readPRState(readCtx, owner, repoName, item.PRNumber)
		cancel()
		if err != nil {
			log.Printf("autonomous: pr-recheck: %s#%d PR #%d still unreadable (%v) — will re-check next scan",
				item.Repo, item.Number, item.PRNumber, err)
			return
		}
		if strings.EqualFold(prState, "MERGED") {
			as.recoverMergedPR(ctx, item)
			return
		}
	}
	// The revert can write Ready, so it never runs for a closed issue (#2750).
	owner, repoName := splitOwnerRepo(item.Repo)
	readCtx, cancel := context.WithTimeout(ctx, boardRecoveryTimeout)
	closed, err := as.orphanIssueClosed(readCtx, owner, repoName, item.Number)
	cancel()
	if err != nil {
		log.Printf("autonomous: pr-recheck: %s#%d issue state still unreadable (%v) — will re-check next scan",
			item.Repo, item.Number, err)
		return
	}
	if closed {
		as.moveIssueToDone(ctx, item.Repo, item.Number)
	} else if !as.revertFailedIssueStatus(ctx, item.Repo, item.Number, item.Kind) {
		return // the revert could not list open PRs; it kept the entry
	}
	as.mu.Lock()
	as.removePRRecheckLocked(item.Repo, item.Number)
	as.persistStateLocked()
	as.mu.Unlock()
}

// recoverMergedPR runs the post-merge path for a PR that merged after its run
// died, and records the issue completed. The entry is claimed under the lock
// before the post-merge path runs, so a second sweep never runs it again.
func (as *AutonomousScheduler) recoverMergedPR(ctx context.Context, item PRRecheckItem) {
	postMerge := as.postMergeFn
	if postMerge == nil {
		if as.scheduler == nil {
			log.Printf("autonomous: pr-recheck: %s#%d PR #%d is MERGED but no scheduler is wired to run its post-merge path — keeping it",
				item.Repo, item.Number, item.PRNumber)
			return
		}
		postMerge = func(ctx context.Context, bi types.BoardItem, prNumber int) hooks.PostMergeResult {
			return as.scheduler.runPostMerge(ctx, bi, nil, prNumber)
		}
	}

	key := fmt.Sprintf("%s#%d", item.Repo, item.Number)
	as.mu.Lock()
	if !as.removePRRecheckLocked(item.Repo, item.Number) {
		as.mu.Unlock()
		return // another sweep claimed it
	}
	title := ""
	keptFailed := as.state.Failed[:0]
	for _, f := range as.state.Failed {
		if f.Repo == item.Repo && f.Number == item.Number {
			title = f.Title
			continue
		}
		keptFailed = append(keptFailed, f)
	}
	as.state.Failed = keptFailed
	delete(as.retryBackoff, key)
	completed := false
	for _, c := range as.state.Completed {
		if c.Repo == item.Repo && c.Number == item.Number {
			completed = true
			break
		}
	}
	if !completed {
		as.state.Completed = append(as.state.Completed, CompletedItem{
			Repo:        item.Repo,
			Number:      item.Number,
			Title:       title,
			CompletedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	if as.recentClosures == nil {
		as.recentClosures = make(map[string]time.Time)
	}
	as.recentClosures[key] = time.Now()
	as.persistStateLocked()
	as.mu.Unlock()

	log.Printf("autonomous: pr-recheck: %s#%d PR #%d merged while its run was down — running the post-merge path",
		item.Repo, item.Number, item.PRNumber)
	res := postMerge(ctx, types.BoardItem{Repo: item.Repo, Number: item.Number, Title: title}, item.PRNumber)
	log.Printf("autonomous: pr-recheck: %s#%d post-merge done (issue closed %v, validation marker %q)",
		item.Repo, item.Number, res.IssueClosed, res.ValidationMarker)

	// EvaluatePostMerge moves the issue to Done on the scheduler's own board
	// only. Zeroing the pace makes the rollup sweep that runs next in this
	// cycle bring every configured board to Done.
	as.mu.Lock()
	as.lastEpicRollupSweepAt = time.Time{}
	as.mu.Unlock()
}

// readPRState reads a PR's state through the seam or the forge client.
func (as *AutonomousScheduler) readPRState(ctx context.Context, owner, repoName string, number int) (string, error) {
	if as.prStateFn != nil {
		return as.prStateFn(ctx, owner, repoName, number)
	}
	if as.ghClient == nil {
		return "", fmt.Errorf("no forge client")
	}
	return gh.NewPRService(as.ghClient).GetPRState(ctx, owner, repoName, number)
}
