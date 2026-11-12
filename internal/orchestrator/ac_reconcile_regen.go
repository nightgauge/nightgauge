package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// regenerateACReconcile rebuilds ac-reconcile-{N}.json in ws from the issue
// body readBody returns, replacing whatever an earlier run left there (#3228).
// The report is the scope every later stage judges, so it is produced by the
// orchestrator from the body as it is now, never by the planning model and
// never reused: on #3192's re-run the model found the previous run's 9-criterion
// report, read it, and four criteria added since were never judged.
//
// Any error is returned unchanged in meaning: the caller fails the stage. The
// old file is not a fallback — a report that may predate the body is the
// defect this exists to remove.
func regenerateACReconcile(ctx context.Context, ws string, issueNumber int, readBody func(context.Context) (string, error)) (*preflight.ACReconcileResult, error) {
	body, err := readBody(ctx)
	if err != nil {
		return nil, fmt.Errorf("read issue #%d body: %w", issueNumber, err)
	}
	outPath, err := stagecontext.ContextPath(ws, issueNumber, "ac-reconcile")
	if err != nil {
		return nil, fmt.Errorf("resolve ac-reconcile path: %w", err)
	}
	rec := preflight.ReconcileACs(ws, issueNumber, body)
	if err := preflight.WriteACReconcile(rec, outPath); err != nil {
		return nil, err
	}
	return rec, nil
}

// regenerateACReconcileFor runs regenerateACReconcile for a feature-planning
// dispatch and, when the body has no criteria, posts the one-time note the
// skill's `--notify-empty` used to post (#2906). The note is best-effort; the
// regeneration is not.
func (s *Scheduler) regenerateACReconcileFor(ctx context.Context, item types.BoardItem, ws string) error {
	rec, err := regenerateACReconcile(ctx, ws, item.Number, func(ctx context.Context) (string, error) {
		return s.readACReconcileBody(ctx, item)
	})
	if err != nil {
		return err
	}
	log.Printf("#%d: ac-reconcile regenerated from the current issue body (%d criteria, aggregate=%s)",
		item.Number, len(rec.AcceptanceCriteria), rec.AggregateStatus)
	if rec.AggregateStatus == "no-acs-detected" {
		if err := s.notifyNoACs(ctx, item); err != nil {
			log.Printf("#%d: ac-reconcile: no acceptance criteria, and the note could not be posted: %v", item.Number, err)
		}
	}
	return nil
}

// readACReconcileBody reads the dispatched issue's body and then decides,
// after the read and on a fresh cache, whether the issue is still vouched
// for: the same guard `forge issue view --vouched` applies (#2130). The
// per-stage re-check ran before this read, so an outsider's edit landing in
// between would otherwise reach the reconcile unvouched (#3231). A refused or
// undecidable issue (no gate, forge cannot answer) fails closed with an error,
// so no report is written from the body.
func (s *Scheduler) readACReconcileBody(ctx context.Context, item types.BoardItem) (string, error) {
	var body string
	var err error
	if s.acReconcileBody != nil {
		body, err = s.acReconcileBody(ctx, item)
	} else {
		body, err = forgeACReconcileBody(s, ctx, item)
	}
	if err != nil {
		return "", err
	}
	if d := s.stageEligibility(ctx, item); !d.Eligible {
		return "", fmt.Errorf("issue body not vouched for at read time: %s", d.Reason)
	}
	return body, nil
}

// forgeACReconcileBody and forgeNotifyNoACs are the production forge calls
// behind readACReconcileBody and notifyNoACs. Package variables so the test
// binary's TestMain keeps every unstubbed pipeline test off the network.
var (
	forgeACReconcileBody = (*Scheduler).readIssueBodyFromForge
	forgeNotifyNoACs     = (*Scheduler).postNoACsNote
)

// readIssueBodyFromForge is the raw read; readACReconcileBody vouches for its
// result after the read.
func (s *Scheduler) readIssueBodyFromForge(ctx context.Context, item types.BoardItem) (string, error) {
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("repository %q is not owner/name", item.Repo)
	}
	svc := s.issueServiceFor(ctx, owner, repo)
	if svc == nil {
		return "", errors.New("no issue client configured")
	}
	issue, err := svc.GetIssueWithRelations(ctx, owner, repo, item.Number, gh.NoRelations)
	if err != nil {
		return "", err
	}
	if issue == nil {
		return "", errors.New("issue not found")
	}
	return issue.Body, nil
}

// notifyNoACs posts preflight.NoACsComment once per issue.
func (s *Scheduler) notifyNoACs(ctx context.Context, item types.BoardItem) error {
	if s.acEmptyNotify != nil {
		return s.acEmptyNotify(ctx, item)
	}
	return forgeNotifyNoACs(s, ctx, item)
}

func (s *Scheduler) postNoACsNote(ctx context.Context, item types.BoardItem) error {
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" || repo == "" {
		return fmt.Errorf("repository %q is not owner/name", item.Repo)
	}
	client, err := s.repoClient(ctx, owner, repo)
	if err != nil {
		return err
	}
	filer := followup.NewGitHubFiler(client, owner, repo, 0, gh.OwnerType(""))
	posted, err := filer.HasComment(ctx, item.Number, preflight.NoACsMarker)
	if err != nil || posted {
		return err
	}
	return filer.AddComment(ctx, item.Number, preflight.NoACsComment(item.Number))
}
