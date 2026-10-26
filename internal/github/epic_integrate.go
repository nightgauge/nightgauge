package github

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/git"
)

// Epic integration outcomes (#2560). Only IntegrationNoBranch,
// IntegrationAlreadyOnBase and IntegrationMerged allow an epic to close: every
// other outcome means the epic branch holds commits the base does not, and an
// epic closed then would hide work that never reached main (#2085).
const (
	IntegrationNoBranch      = "no_epic_branch"
	IntegrationAlreadyOnBase = "already_on_base"
	IntegrationMerged        = "merged"
	IntegrationPending       = "pending"
	IntegrationConflict      = "conflict"
	IntegrationCIFailed      = "ci_failed"
	IntegrationNoCheckout    = "no_checkout"
	IntegrationError         = "error"
)

// EpicIntegration reports how an epic branch was brought onto its base.
type EpicIntegration struct {
	Outcome       string   `json:"outcome"`
	Branch        string   `json:"branch,omitempty"`
	PRURL         string   `json:"prUrl,omitempty"`
	PRNumber      int      `json:"prNumber,omitempty"`
	ConflictFiles []string `json:"conflictFiles,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// AllowsClose reports whether the epic may be closed after this integration.
func (i EpicIntegration) AllowsClose() bool {
	switch i.Outcome {
	case IntegrationNoBranch, IntegrationAlreadyOnBase, IntegrationMerged:
		return true
	}
	return false
}

// Blocked reports whether integration failed in a way that needs an operator:
// anything that is neither closable nor merely pending.
func (i EpicIntegration) Blocked() bool {
	return !i.AllowsClose() && i.Outcome != IntegrationPending
}

// EpicCheckoutResolver opens the checkout of epicRepo ("owner/name"), the only
// repository in which epic/<N>-* is the branch of epic #N (#2377).
type EpicCheckoutResolver func(epicRepo string) (*git.Service, error)

// CheckoutResolverForPath returns a resolver that serves only the checkout at
// path, and only when its origin is the epic's repository (#2388).
func CheckoutResolverForPath(path string) EpicCheckoutResolver {
	return func(epicRepo string) (*git.Service, error) {
		svc, err := git.NewService(path)
		if err != nil {
			return nil, err
		}
		if err := svc.RequireOriginRepo(epicRepo); err != nil {
			return nil, err
		}
		return svc, nil
	}
}

// SetCheckoutResolver installs the resolver every epic close uses to integrate
// the epic branch first. Without one, an epic whose branch cannot be inspected
// is never closed.
func (e *EpicService) SetCheckoutResolver(fn EpicCheckoutResolver) {
	e.checkout = fn
}

// epicPRMerger is the PR surface integration needs; *PRService satisfies it.
type epicPRMerger interface {
	CreateEpicPR(ctx context.Context, owner, repo string, epicNumber int, epicTitle, epicBranch, baseBranch string) (*EpicPRResult, error)
	MergeEpicPR(ctx context.Context, owner, repo string, prNodeID, epicBranch string) error
}

// integrateEpic brings ref's epic branch onto main: merge main into the epic
// branch (resolving CHANGELOG-only conflicts), open or reuse the epic PR, and
// merge it — through the merge queue when the base requires one. It is the one
// implementation behind every epic close (#2560).
func (e *EpicService) integrateEpic(ctx context.Context, ref EpicRef, title string) EpicIntegration {
	if e.checkout == nil {
		return EpicIntegration{Outcome: IntegrationNoCheckout, Error: "no checkout resolver configured"}
	}
	gitSvc, err := e.checkout(ref.Owner + "/" + ref.Repo)
	if err != nil {
		return EpicIntegration{Outcome: IntegrationNoCheckout, Error: err.Error()}
	}
	var prs epicPRMerger
	if e.prs != nil {
		prs = e.prs
	} else {
		prs = NewPRService(e.client)
	}
	return integrateEpicWith(ctx, gitSvc, prs, ref, title, "main")
}

func integrateEpicWith(ctx context.Context, gitSvc *git.Service, prs epicPRMerger, ref EpicRef, title, base string) EpicIntegration {
	if err := gitSvc.Fetch(false); err != nil {
		return EpicIntegration{Outcome: IntegrationError, Error: err.Error()}
	}
	branch, err := gitSvc.FindEpicBranch(ref.Number)
	if err != nil {
		return EpicIntegration{Outcome: IntegrationNoBranch}
	}
	res := EpicIntegration{Branch: branch}

	ahead, err := gitSvc.EpicBranchAhead(branch, base)
	if err != nil {
		res.Outcome, res.Error = IntegrationError, err.Error()
		return res
	}
	if ahead == 0 {
		res.Outcome = IntegrationAlreadyOnBase
		cleanupEpicBranch(gitSvc, branch)
		return res
	}

	merge, err := gitSvc.MergeBaseIntoBranch(branch, base)
	if err != nil {
		res.Outcome, res.Error = IntegrationError, err.Error()
		return res
	}
	if merge.Outcome == git.BaseMergeConflict {
		res.Outcome, res.ConflictFiles = IntegrationConflict, merge.ConflictFiles
		return res
	}

	pr, err := prs.CreateEpicPR(ctx, ref.Owner, ref.Repo, ref.Number, title, branch, base)
	if err != nil {
		res.Outcome, res.Error = IntegrationError, err.Error()
		return res
	}
	res.PRURL, res.PRNumber = pr.PRURL, pr.PRNumber
	if pr.PRNodeID == "" {
		// A merged PR with commits still ahead of base: the branch moved on
		// after the merge, and those commits are not on main.
		res.Outcome, res.Error = IntegrationError, fmt.Sprintf("epic PR is %s but %s has %d commit(s) not on %s", pr.Action, branch, ahead, base)
		return res
	}
	if err := prs.MergeEpicPR(ctx, ref.Owner, ref.Repo, pr.PRNodeID, branch); err != nil {
		var mq *MergeQueueError
		if errors.As(err, &mq) && mq.Kind == MergeQueueTimeout {
			res.Outcome = IntegrationPending
		} else if errors.As(err, &mq) {
			res.Outcome = IntegrationCIFailed
		} else {
			res.Outcome = IntegrationError
		}
		res.Error = err.Error()
		return res
	}
	res.Outcome = IntegrationMerged
	cleanupEpicBranch(gitSvc, branch)
	return res
}

func cleanupEpicBranch(gitSvc *git.Service, branch string) {
	if err := gitSvc.BranchCleanup(branch); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: epic branch cleanup %s: %v\n", branch, err)
	}
}
