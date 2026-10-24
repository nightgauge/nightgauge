// Package orchestrator — eligibility.go connects the scheduler to the
// eligibility gate (internal/trust, #2130). An issue is worked only when a
// user with write access or higher vouches for it, by authoring it or by
// applying `pipeline:approved`. Every entry point asks the same gate:
// refinement, Backlog-to-Ready promotion, the dispatch scan (PickNext), the
// queue (RunQueue), wave sub-issues, IPC pipeline.runItem and `nightgauge
// run <issue>`.
package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// trustedBotsFrom reads autonomous.trusted_bots; empty means the gate's
// default (the pipeline's own App and github-actions).
func trustedBotsFrom(cfg *config.Config) []string {
	if cfg == nil || cfg.Autonomous == nil {
		return nil
	}
	return cfg.Autonomous.TrustedBots
}

// newSchedulerTrustGate builds the scheduler's gate. Each repository is read
// with the client the scheduler's resolver gives it, falling back to the
// startup client, the same identity the scheduler reads the issue with.
func newSchedulerTrustGate(s *Scheduler, trustedBots, appOwners, configuredLogins []string) *trust.Gate {
	forge := gh.NewTrustForgeResolver(func(ctx context.Context, owner, repo string) (*gh.Client, error) {
		if s.clientResolver != nil {
			if c, err := s.clientResolver(ctx, owner, repo); err == nil && c != nil {
				return c, nil
			}
		}
		if s.client == nil {
			return nil, fmt.Errorf("no GitHub client configured")
		}
		return s.client, nil
	})
	if len(appOwners) == 0 {
		log.Printf("eligibility: label approval (%s) is unavailable: no GitHub App installation is configured, so "+
			"the pipeline's stages act as a personal token and a label they applied could not be told apart from a "+
			"maintainer's. Only issues opened by users with write access are eligible (#2130)", trust.ApprovedLabel)
	} else {
		log.Printf("eligibility: label approval (%s) is available for %v, where the pipeline acts as a GitHub App; "+
			"elsewhere only issues opened by users with write access are eligible (#2130)", trust.ApprovedLabel, appOwners)
	}
	return trust.NewGate(forge, trustedBots).WithAppOwners(appOwners...).
		WithStageIdentities(func(ctx context.Context) ([]string, error) {
			return gh.StageIdentities(ctx, configuredLogins)
		})
}

// SetTrustGate replaces the scheduler's eligibility gate. Tests use it to
// read permissions from a fake.
func (s *Scheduler) SetTrustGate(g *trust.Gate) { s.trustGate = g }

// resetTrustCycle starts a new cache cycle, so a permission or approval change
// takes effect on the next scan.
func (s *Scheduler) resetTrustCycle() {
	if s != nil && s.trustGate != nil {
		s.trustGate.ResetCycle()
	}
}

// decide asks the gate. A scheduler without a gate refuses: fail closed.
func (s *Scheduler) decide(ctx context.Context, subj trust.Subject) trust.Decision {
	if s == nil || s.trustGate == nil {
		return trust.Decision{Reason: fmt.Sprintf("%s/%s#%d is not eligible: no eligibility gate is configured",
			subj.Owner, subj.Repo, subj.Number)}
	}
	return s.trustGate.Decide(ctx, subj)
}

// decideItem decides a board item.
func (s *Scheduler) decideItem(ctx context.Context, item types.BoardItem) trust.Decision {
	owner, repo := splitOwnerRepo(item.Repo)
	return s.decide(ctx, trust.Subject{
		Owner: owner, Repo: repo, Number: item.Number,
		Author: item.AuthorLogin, Labels: item.Labels, LabelsComplete: !item.LabelsTruncated,
	})
}

// decideNode decides a dependency-graph node.
func (s *Scheduler) decideNode(ctx context.Context, node *depgraph.Node) trust.Decision {
	owner, repo := splitOwnerRepo(node.Repo)
	return s.decide(ctx, trust.Subject{
		Owner: owner, Repo: repo, Number: node.Number,
		Author: node.AuthorLogin, Labels: node.Labels, LabelsComplete: !node.LabelsTruncated,
	})
}

// Eligibility answers for a manual dispatch (IPC pipeline.runItem, `nightgauge
// run <issue>`). It starts a fresh cycle first, so an approval applied a
// moment ago counts, and reads the issue itself: a manual dispatch carries no
// trusted author or label set.
func (s *Scheduler) Eligibility(ctx context.Context, owner, repo string, number int) trust.Decision {
	subj := trust.Subject{Owner: owner, Repo: repo, Number: number}
	if s == nil || s.trustGate == nil {
		return s.decide(ctx, subj)
	}
	return s.trustGate.Fresh().Decide(ctx, subj)
}

// stageEligibility re-decides a running item before each stage, on a fresh
// cache (#2130). A run is long, and an approved issue edited by someone who
// cannot vouch for it while the run is in flight must stop the run before the
// next stage reads the edited text.
func (s *Scheduler) stageEligibility(ctx context.Context, item types.BoardItem) trust.Decision {
	owner, repo := splitOwnerRepo(item.Repo)
	if owner == "" {
		owner = s.owner
	}
	return s.Eligibility(ctx, owner, repo, item.Number)
}

// nodeEligible adapts the gate to the promotion predicates.
func (s *Scheduler) nodeEligible(ctx context.Context) func(*depgraph.Node) bool {
	return func(n *depgraph.Node) bool { return s.decideNode(ctx, n).Eligible }
}

// eligibility is the scheduler whose gate the autonomous loop asks. With no
// scheduler and no override it is nil, and a nil scheduler refuses.
func (as *AutonomousScheduler) eligibility() *Scheduler {
	if as.trustGateOverride != nil {
		return &Scheduler{trustGate: as.trustGateOverride}
	}
	return as.scheduler
}

// pausedNotVouchedFor is the queue pause kind for an item the eligibility gate
// refused; the next dequeue releases it once someone vouches for it.
const pausedNotVouchedFor = "not_vouched_for"

// queueEligibility decides every queued item that could claim a slot, keyed
// by repoIssueKey. Called without s.mu held: the gate may read the forge. A
// queue item may name its repository without an owner; it is read in the
// scheduler's owner.
func (s *Scheduler) queueEligibility(ctx context.Context) map[string]trust.Decision {
	s.mu.Lock()
	items := make([]QueueItem, 0, len(s.queue))
	for _, it := range s.queue {
		held := it.Status == "paused" && it.PausedReason != nil && it.PausedReason.Kind == pausedNotVouchedFor
		if (it.Status != "paused" && it.Status != "processing") || held {
			items = append(items, it)
		}
	}
	s.mu.Unlock()

	out := make(map[string]trust.Decision, len(items))
	for _, it := range items {
		owner, repo := splitOwnerRepo(it.Repo)
		if owner == "" {
			owner = s.owner
		}
		out[repoIssueKey(it.Repo, it.IssueNumber)] = s.decide(ctx, trust.Subject{
			Owner: owner, Repo: repo, Number: it.IssueNumber,
			Labels: it.Labels, LabelsComplete: !it.LabelsTruncated && it.Labels != nil,
		})
	}
	return out
}
