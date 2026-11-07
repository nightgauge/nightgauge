package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// The architecture-hold release (#2717).
//
// An architecture-approval halt parks the issue in In progress (no PR) or In
// review, and the scheduler dispatches neither. Approval arrives three ways —
// the Action Center card, a directly applied label, an approval file — and
// before #2717 none of them moved the board: the reconcile retracted the card
// and dropped the Failed entry, and the issue stayed parked with nothing left
// to say so. This file is the one release path all three take. It moves the
// issue to Ready on every board that carries it, and keeps the Failed entry,
// marked ReleasePending, until every move lands, so a failed write is retried
// on the next reconcile and is never reported as a release.

// beginArchitectureReleaseLocked starts the release of an architecture hold
// whose approval has just been observed: it retracts the card, whose lifetime
// is the hold's (#2577), and schedules the board move. It returns the entry to
// keep in the Failed list.
//
// Caller MUST hold as.mu.
func (as *AutonomousScheduler) beginArchitectureReleaseLocked(f FailedItem) FailedItem {
	repo, number := f.Repo, f.Number
	as.dispatchAttention(func() { as.retractArchitectureApproval(repo, number) })
	f.ReleasePending = true
	return as.continueArchitectureReleaseLocked(f, "approval landed")
}

// continueArchitectureReleaseLocked schedules the board move for a
// ReleasePending entry unless one is already in flight, and returns the entry.
//
// Caller MUST hold as.mu.
func (as *AutonomousScheduler) continueArchitectureReleaseLocked(f FailedItem, why string) FailedItem {
	key := fmt.Sprintf("%s#%d", f.Repo, f.Number)
	if as.architectureReleaseInFlight[key] {
		return f
	}
	if as.architectureReleaseInFlight == nil {
		as.architectureReleaseInFlight = make(map[string]bool)
	}
	as.architectureReleaseInFlight[key] = true
	repo, number := f.Repo, f.Number
	log.Printf("autonomous: architecture-release: %s — %s; scheduling the move to Ready", key, why)
	as.goTrackedBoardOp(func(ctx context.Context) {
		defer func() {
			as.mu.Lock()
			delete(as.architectureReleaseInFlight, key)
			as.mu.Unlock()
		}()
		as.releaseArchitectureHold(ctx, repo, number)
	})
	return f
}

// ReleaseArchitectureHold is the Action Center card's entry into the shared
// release: after the card applies the approval label, it asks for the same
// release the reconcile performs when it sees that label itself. It reports
// whether the issue had an architecture hold to release; false means there was
// none recorded, and the label alone stands for the next run's gate.
func (as *AutonomousScheduler) ReleaseArchitectureHold(repo string, issue int) bool {
	if as == nil {
		return false
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	for i, f := range as.state.Failed {
		if f.Repo != repo || f.Number != issue || f.hold() != HoldArchitectureApproval {
			continue
		}
		if f.ReleasePending {
			as.state.Failed[i] = as.continueArchitectureReleaseLocked(f, "approved from the Action Center (release already pending)")
		} else {
			as.state.Failed[i] = as.beginArchitectureReleaseLocked(f)
		}
		as.persistStateLocked()
		return true
	}
	return false
}

// architectureReleaseBoard is one project board the released issue is moved on.
type architectureReleaseBoard struct {
	project   int
	ownerType gh.OwnerType
}

// releaseArchitectureHold moves a released architecture hold to Ready on every
// configured board that carries the issue's repository, then drops its Failed
// entry. It does network I/O and must be called without as.mu held.
//
// Guards, each read fresh here rather than at reconcile time:
//   - an issue with an active run is not moved: the run owns its status;
//   - an issue with an open or merged PR of its own is not moved: Ready would
//     re-dispatch work that already produced a PR (#1707, #2706);
//   - if either PR list cannot be read, nothing is moved and the entry stays,
//     because "unreadable" is not "no PR".
//
// In the first two cases the hold has nothing left to wait for, so the entry is
// dropped, as it was before #2717. Any failed board write leaves the entry for
// the next reconcile to retry, and is logged as a failure, never a release.
func (as *AutonomousScheduler) releaseArchitectureHold(parent context.Context, repo string, issue int) {
	key := fmt.Sprintf("%s#%d", repo, issue)
	owner, repoName := splitOwnerRepo(repo)

	as.mu.Lock()
	running := as.issueRunningLocked(repo, issue)
	boards := as.boardsForRepoLocked(owner, repoName)
	as.mu.Unlock()

	if running {
		log.Printf("autonomous: architecture-release: NOT moving %s to Ready — a run is active for it; dropping the hold", key)
		as.dropArchitectureRelease(repo, issue)
		return
	}
	if len(boards) == 0 {
		log.Printf("autonomous: architecture-release: no project board configured for %s — dropping the hold for %s with no board move", repo, key)
		as.dropArchitectureRelease(repo, issue)
		return
	}

	ctx, cancel := context.WithTimeout(parent, boardRecoveryTimeout)
	defer cancel()

	pr, found, err := as.ownOpenPR(ctx, owner, repoName, issue)
	if err != nil {
		log.Printf("autonomous: architecture-release: WARNING not moving %s to Ready yet — could not list open PRs (%v); will retry on the next reconcile", key, err)
		return
	}
	if !found {
		pr, found, err = as.ownMergedPR(ctx, owner, repoName, issue)
		if err != nil {
			log.Printf("autonomous: architecture-release: WARNING not moving %s to Ready yet — could not list merged PRs (%v); will retry on the next reconcile", key, err)
			return
		}
	}
	if found {
		log.Printf("autonomous: architecture-release: NOT moving %s to Ready — PR #%d of its own exists; dropping the hold", key, pr)
		as.dropArchitectureRelease(repo, issue)
		return
	}

	var failed []string
	for _, b := range boards {
		if err := as.moveReleasedIssue(ctx, owner, repoName, b, issue); err != nil {
			failed = append(failed, fmt.Sprintf("project %d: %v", b.project, err))
		}
	}
	if len(failed) > 0 {
		log.Printf("autonomous: architecture-release: WARNING could not move %s to Ready (%s); the hold stays pending and the next reconcile retries",
			key, strings.Join(failed, "; "))
		return
	}
	if as.dropArchitectureRelease(repo, issue) {
		log.Printf("autonomous: architecture-release: released %s → Ready on %d board(s)", key, len(boards))
	}
}

// moveReleasedIssue moves the issue to Ready on one board.
func (as *AutonomousScheduler) moveReleasedIssue(ctx context.Context, owner, repoName string, b architectureReleaseBoard, issue int) error {
	if as.releaseMoveStatus != nil {
		return as.releaseMoveStatus(ctx, owner, repoName, b.project, b.ownerType, issue, "Ready")
	}
	return as.projectService(owner, b.project, b.ownerType).MoveStatus(ctx, owner, repoName, issue, "Ready")
}

// ownMergedPR reports a merged PR whose head branch names the issue, read
// from the repository's newest merged PRs (one page, mergedPRIndexSize). A
// merged PR outside that window is not seen: its issue would normally be
// closed by it, and the reconcile prunes closed issues before this runs.
// err is non-nil when the list could not be read.
func (as *AutonomousScheduler) ownMergedPR(ctx context.Context, owner, repoName string, issue int) (int, bool, error) {
	if as.ghClient == nil {
		return 0, false, fmt.Errorf("no forge client")
	}
	heads, err := gh.NewPRService(as.ghClient).ListMergedPRHeads(ctx, owner, repoName, 0)
	if err != nil {
		return 0, false, err
	}
	for _, h := range heads {
		if n, ok := git.ParseIssueNumberFromBranch(h.HeadRefName); ok && n == issue {
			return h.Number, true, nil
		}
	}
	return 0, false, nil
}

// dropArchitectureRelease removes the issue's ReleasePending architecture
// entry from the Failed list and reports whether it was there. An entry the
// operator cleared, or one replaced by a newer failure, is left alone.
func (as *AutonomousScheduler) dropArchitectureRelease(repo string, issue int) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	for i, f := range as.state.Failed {
		if f.Repo == repo && f.Number == issue && f.hold() == HoldArchitectureApproval && f.ReleasePending {
			as.state.Failed = append(as.state.Failed[:i:i], as.state.Failed[i+1:]...)
			as.persistStateLocked()
			return true
		}
	}
	return false
}

// issueRunningLocked reports whether the issue has an active run.
//
// Caller MUST hold as.mu.
func (as *AutonomousScheduler) issueRunningLocked(repo string, issue int) bool {
	for _, r := range as.state.Running {
		if r.Repo == repo && r.Number == issue {
			return true
		}
	}
	return false
}

// boardsForRepoLocked returns every distinct project board configured for the
// repository. The other movers take the first match; a release must reach
// every board, or the issue stays parked on the ones it missed.
//
// Caller MUST hold as.mu.
func (as *AutonomousScheduler) boardsForRepoLocked(owner, repoName string) []architectureReleaseBoard {
	var out []architectureReleaseBoard
	seen := map[architectureReleaseBoard]bool{}
	for _, rc := range as.repos {
		if rc.Owner != owner || rc.Name != repoName || rc.Project <= 0 {
			continue
		}
		b := architectureReleaseBoard{project: rc.Project, ownerType: rc.OwnerType}
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}
