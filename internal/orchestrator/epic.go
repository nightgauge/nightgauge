package orchestrator

import (
	"context"
	"log"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// checkEpicCompletion explicitly closes the merged sub-issue and, if its
// parent epic now has all sub-issues closed, auto-closes the epic with a
// comment and a project board move to Done. Fires OnEpicComplete on epic
// closure.
//
// Delegates to hooks.EvaluatePostMerge so the deterministic CLI path
// (`nightgauge hook post-merge`) and the in-process scheduler path
// share one implementation. EvaluatePostMerge closes the sub-issue
// explicitly — important because GitHub's auto-close keyword may not have
// propagated by the time this runs, in which case GetEpicProgress would
// still see the sub-issue as OPEN and the epic check would no-op.
//
// prNumber is the GitHub PR number associated with the merge (0 = unknown,
// which skips the PR-state guard in EvaluatePostMerge).
func (s *Scheduler) checkEpicCompletion(ctx context.Context, item types.BoardItem, prNumber int) hooks.PostMergeResult {
	ownerPart, repoPart := splitOwnerRepo(item.Repo)
	epicSvc := gh.NewEpicService(s.client)
	var prVerifier hooks.PRVerifier
	var boardSvc hooks.BoardSyncer
	if s.client != nil {
		prVerifier = gh.NewPRService(s.client)
		if s.projectNumber > 0 {
			boardSvc = gh.NewProjectService(s.client, ownerPart, s.projectNumber)
		}
	}

	result := hooks.EvaluatePostMerge(ctx, s.issueSvc, s.issueSvc, epicSvc, prVerifier, boardSvc, hooks.PostMergeInput{
		IssueNumber:     item.Number,
		RepositoryOwner: ownerPart,
		RepositoryName:  repoPart,
		ProjectNumber:   s.projectNumber,
		PRNumber:        prNumber,
	})

	if result.AutoClosed {
		// Epic fully closed (all sub-issues merged) — bridge "closed" → "ready to
		// ship" with a Discord notification carrying the deploy dispatch command.
		// Notify-only; never auto-submits to stores (#4076). Fire-and-forget on a
		// DETACHED, bounded context: this runs on the pr-merge finalization path,
		// so a hung webhook must not block the pipeline (the pipeline ctx may also
		// be cancelled once the run returns) (#4076 review).
		repo, epic := item.Repo, result.EpicNumber
		go func() {
			nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
			defer cancel()
			s.emitReadyToShipAlert(nctx, repo, epic)
		}()
		if s.onEpicComplete != nil {
			s.onEpicComplete(item.Repo, result.EpicNumber)
		}
	}

	log.Printf("#%d: post-merge hook: issueClosed=%v autoClosed=%v epic=#%d reason=%s",
		item.Number, result.IssueClosed, result.AutoClosed, result.EpicNumber, result.Reason)
	return result
}

// GetEpicSubIssuesByRepo groups an epic's sub-issues by repository for cross-repo dispatch.
func (s *Scheduler) GetEpicSubIssuesByRepo(ctx context.Context, epicOwner, epicRepo string, epicNumber int) (map[string][]types.SubIssueRef, error) {
	epic, err := s.issueSvc.GetEpicProgressByNumber(ctx, epicOwner, epicRepo, epicNumber)
	if err != nil {
		return nil, err
	}

	byRepo := make(map[string][]types.SubIssueRef)
	for _, si := range epic.SubIssues {
		byRepo[si.Repo] = append(byRepo[si.Repo], si)
	}
	return byRepo, nil
}

// FindReadySubIssues returns sub-issues of an epic that are ready and unblocked.
func (s *Scheduler) FindReadySubIssues(ctx context.Context, epicOwner, epicRepo string, epicNumber int) ([]types.SubIssueRef, error) {
	epic, err := s.issueSvc.GetEpicProgressByNumber(ctx, epicOwner, epicRepo, epicNumber)
	if err != nil {
		return nil, err
	}

	var ready []types.SubIssueRef
	for _, si := range epic.SubIssues {
		if !strings.EqualFold(si.State, "OPEN") {
			continue
		}

		// Check if this sub-issue is blocked
		siOwner, siRepo := splitOwnerRepo(si.Repo)
		siIssue, err := s.issueSvc.GetIssue(ctx, siOwner, siRepo, si.Number)
		if err != nil {
			log.Printf("warn: failed to check sub-issue #%d: %v", si.Number, err)
			continue
		}

		blocked := false
		for _, blocker := range siIssue.BlockedBy {
			if strings.EqualFold(blocker.State, "OPEN") {
				blocked = true
				break
			}
		}

		if !blocked {
			ready = append(ready, si)
		}
	}

	return ready, nil
}
