package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// placementReader reads one issue's board placement on one board (#3278).
type placementReader interface {
	Placement(ctx context.Context, owner, repo string, number, project int) (gh.BoardPlacement, error)
}

// ghPlacementReader is the GitHub-backed placementReader.
type ghPlacementReader struct{ client *gh.Client }

func (r ghPlacementReader) Placement(ctx context.Context, owner, repo string, number, project int) (gh.BoardPlacement, error) {
	return gh.IssueBoardPlacement(ctx, r.client, owner, repo, number, project)
}

// readinessChecker answers "will the scheduler dispatch this issue?" for the
// CLI, with the scheduler's own rules (orchestrator.DispatchSkipReasons) and
// the same config resolution the scheduler is built with.
type readinessChecker struct {
	reader            placementReader
	project           int // 0: the repo has no board, so nothing on it dispatches
	excludeLabels     []string
	pickupBacklog     bool
	refinementBacklog bool
}

// newReadinessChecker resolves the autonomous config the scheduler would use.
func newReadinessChecker(cfg *config.Config, reader placementReader, project int) readinessChecker {
	auto := buildAutonomousConfig(cfg, autonomousConfigOverrides{})
	return readinessChecker{
		reader:            reader,
		project:           project,
		excludeLabels:     auto.ExcludeLabels,
		pickupBacklog:     auto.PickupBacklog,
		refinementBacklog: auto.RefinementBacklog,
	}
}

// readinessResult is one issue's board placement and skip reasons.
type readinessResult struct {
	Placement gh.BoardPlacement
	Reasons   []orchestrator.SkipReason
}

// check reads the issue's placement and evaluates it.
func (c readinessChecker) check(ctx context.Context, owner, repo string, number int) (readinessResult, error) {
	var p gh.BoardPlacement
	if c.project > 0 {
		var err error
		p, err = c.reader.Placement(ctx, owner, repo, number, c.project)
		if err != nil {
			return readinessResult{}, err
		}
	}
	return readinessResult{Placement: p, Reasons: c.evaluate(owner, repo, number, p)}, nil
}

// evaluate applies the scheduler's rules to a placement already in hand.
func (c readinessChecker) evaluate(owner, repo string, number int, p gh.BoardPlacement) []orchestrator.SkipReason {
	return orchestrator.DispatchSkipReasons(orchestrator.ReadinessInput{
		OnBoard:           p.OnBoard,
		BoardStatus:       p.Status,
		Priority:          p.Priority,
		Labels:            p.Labels,
		ExcludeLabels:     c.excludeLabels,
		PickupBacklog:     c.pickupBacklog,
		RefinementBacklog: c.refinementBacklog,
	}, orchestrator.ReadinessTarget{Repo: owner + "/" + repo, Number: number, Project: c.project})
}

// formatSkipReason renders one reason for a human: "code: detail — run: remedy".
func formatSkipReason(r orchestrator.SkipReason) string {
	return fmt.Sprintf("%s: %s — run: %s", r.Code, r.Detail, r.Remedy)
}

// hasLabelFold reports case-insensitive label membership.
func hasLabelFold(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}

// SkipRepoNotInWorkspace is the queue-only refusal for an issue whose
// repository has no checkout this workspace can root a run at (#882, #3278).
const SkipRepoNotInWorkspace = "repo-not-in-workspace"

// repoRootResolver resolves the checkout a run for repo is rooted at
// (Scheduler.RepoRoot).
type repoRootResolver interface {
	RepoRoot(repo string) (string, error)
}

// queueAddRefusals returns why the queue could not run the issue, decided at
// `queue add` time rather than discovered at run time (#3278). The queue runs
// whatever is on its board, so the board's Status and the refinement tier do
// not refuse an explicitly queued issue; what does is a repository the
// workspace cannot run in, an issue missing from the queue's board, or a
// human-only label.
func queueAddRefusals(ctx context.Context, roots repoRootResolver, ready readinessChecker, owner, repo string, number int) ([]orchestrator.SkipReason, error) {
	var out []orchestrator.SkipReason
	full := owner + "/" + repo
	if _, err := roots.RepoRoot(full); err != nil {
		out = append(out, orchestrator.SkipReason{
			Code:   SkipRepoNotInWorkspace,
			Detail: fmt.Sprintf("this workspace has no checkout of %s to run it in", full),
			Remedy: fmt.Sprintf("add %s to .vscode/nightgauge-workspace.yaml repositories[], or queue it from that repository's workspace", full),
		})
	}
	res, err := ready.check(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Reasons {
		if r.Code == orchestrator.SkipNotOnBoard || r.Code == orchestrator.SkipExcludedLabel {
			out = append(out, r)
		}
	}
	return out, nil
}
