package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// issueStateReader reads one issue's state as "OPEN" or "CLOSED" (#3151).
type issueStateReader func(ctx context.Context, owner, repo string, number int) (string, error)

// issueStateGetter is the one-call REST state read. *gh.IssueService
// implements it; the scheduler's issueGetter does not require it, so a test
// fake that lacks it makes the boundary fail open.
type issueStateGetter interface {
	GetIssueState(ctx context.Context, owner, repo string, number int) (string, error)
}

// errNoIssueStateReader is returned when no client can read issue state.
var errNoIssueStateReader = errors.New("no issue-state reader available")

// WithIssueStateReader overrides the stage-boundary issue-state read. Tests
// use it to close an issue between stages; production uses the default.
func (s *Scheduler) WithIssueStateReader(r issueStateReader) {
	s.issueStateRead = r
}

// readIssueState is the default reader: the per-repo client the resolver
// returns (the App installation client when github_auth.app is configured),
// falling back to the scheduler's default issue service.
func (s *Scheduler) readIssueState(ctx context.Context, owner, repo string, number int) (string, error) {
	if s.issueStateRead != nil {
		return s.issueStateRead(ctx, owner, repo, number)
	}
	if s.clientResolver != nil {
		if client, err := s.clientResolver(ctx, owner, repo); err == nil && client != nil {
			return gh.NewIssueService(client).GetIssueState(ctx, owner, repo, number)
		}
	}
	if getter, ok := s.issueSvc.(issueStateGetter); ok && getter != nil {
		return getter.GetIssueState(ctx, owner, repo, number)
	}
	return "", errNoIssueStateReader
}

// issueStateBoundaryApplies reports whether the boundary check runs before
// stage. issue-pickup does its own closed check, and once this run's PR has
// merged a `Closes #N` closes the issue legitimately, so post-merge stages
// must never end as issue_closed.
func issueStateBoundaryApplies(stage state.PipelineStage, runtime *state.RuntimeState) bool {
	if stage == state.StageIssuePickup {
		return false
	}
	if runtime != nil && runtime.Snapshot().MergedAt != "" {
		return false
	}
	return true
}

// issueClosedAtBoundary reads the issue's state before stage starts. It
// fails open: a failed read logs one line and reports open, so an unreachable
// forge never manufactures a terminal. detail is the stage error to book when
// closed; its `issue_closed:` prefix is what the terminal-kind classifier and
// the autonomous non-failure booking key on.
func (s *Scheduler) issueClosedAtBoundary(ctx context.Context, item types.BoardItem, stage state.PipelineStage, runtime *state.RuntimeState) (closed bool, detail string) {
	if !issueStateBoundaryApplies(stage, runtime) {
		return false, ""
	}
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" || repo == "" {
		return false, ""
	}
	st, err := s.readIssueState(ctx, owner, repo, item.Number)
	if err != nil {
		log.Printf("#%d: issue-state read before %s failed (%v) — continuing (fail open)", item.Number, stage, err)
		return false, ""
	}
	if !strings.EqualFold(st, "CLOSED") {
		return false, ""
	}
	return true, issueClosedBoundaryDetail(item.Number, string(stage))
}

func issueClosedBoundaryDetail(number int, stage string) string {
	return fmt.Sprintf("issue_closed: #%d was closed before stage %s started", number, stage)
}
