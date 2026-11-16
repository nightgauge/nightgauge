package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// ProducerUnvalidatedDependency names the card for a dependency that is
// CLOSED but carries no post-merge validation marker, holding a Ready issue
// that nothing else holds (#3081). The hold is correct — the marker says main
// was green after the merge — but it was silent: the dependent waited forever
// and the only clue was a log line.
const ProducerUnvalidatedDependency = "unvalidated-dependency"

func keyUnvalidatedDependency(dep string) string {
	return ProducerUnvalidatedDependency + ":" + dep
}

// unvalidatedOnlyHolds returns the dependencies that hold the issue at key
// solely because they are closed without the validation marker, or nil when
// any other dependency also holds it (an open one, an unresolved one, or one
// whose standing could not be read). Each dependency is judged on its own by
// evaluateDeps, the rule dispatch itself applies.
func unvalidatedOnlyHolds(deps []string, g *depgraph.Graph, resolved map[string]string, validation map[string]ValidationState) []string {
	var held []string
	for _, d := range deps {
		res := evaluateDeps([]string{d}, g, resolved, validation)
		if !res.blocked {
			continue
		}
		if !res.unvalidated || res.status != missingValidationMarkerStatus {
			return nil
		}
		held = append(held, d)
	}
	return held
}

// reconcileUnvalidatedDependencies raises one card per closed, unvalidated
// dependency that is the only thing holding at least one open Ready issue, and
// retracts the card of every dependency that no longer does (#3081). It walks
// the whole graph, so the observed set is complete. Without a validation
// resolver nothing can be judged, and it neither raises nor retracts.
func (as *AutonomousScheduler) reconcileUnvalidatedDependencies(g *depgraph.Graph, adj map[string][]string, resolved map[string]string, validation map[string]ValidationState) {
	if g == nil || as.resolveValidationFn == nil {
		return
	}
	dependents := map[string][]string{}
	for key, node := range g.Nodes {
		if !strings.EqualFold(node.State, "OPEN") || !isReadyStatus(node.BoardStatus) {
			continue
		}
		for _, dep := range unvalidatedOnlyHolds(adj[key], g, resolved, validation) {
			dependents[dep] = append(dependents[dep], key)
		}
	}
	deps := make([]string, 0, len(dependents))
	for dep := range dependents {
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	observed := make([]string, 0, len(deps))
	for _, dep := range deps {
		sort.Strings(dependents[dep])
		if req, ok := BuildUnvalidatedDependencyCard(dep, dependents[dep]); ok {
			observed = append(observed, req.IdempotencyKey)
			as.raiseAttention(req)
		}
	}
	as.autoResolveAttention(ProducerUnvalidatedDependency, observed)
}

// BuildUnvalidatedDependencyCard is the card naming the closed, unvalidated
// dependency dep and the Ready issues it alone holds. Its remedy re-runs the
// post-merge hook for dep (issue.rerunPostMerge), the documented way a merge
// earns its marker once main is green. Standing, keyed by the dependency, so
// every dependent it holds shares one card. ok is false for a key that names
// no issue.
func BuildUnvalidatedDependencyCard(dep string, dependents []string) (attention.DecisionRequest, bool) {
	repo, number, ok := splitNodeKey(dep)
	if !ok || !isOwnerRepo(repo) {
		return attention.DecisionRequest{}, false
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s is closed, but it carries no post-merge validation marker, so the Ready issue(s) below "+
		"wait on it and nothing else:\n\n", dep)
	for _, d := range dependents {
		fmt.Fprintf(&body, "- %s\n", d)
	}
	body.WriteString("\nThe marker is written by the post-merge hook once main is green after the merge. " +
		"**Run post-merge hook** finds the pull request that closed the dependency and runs the hook for it; " +
		"if main is still red the marker is not written and this card stays.")
	return attention.DecisionRequest{
		IdempotencyKey: keyUnvalidatedDependency(dep),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          fmt.Sprintf("%s is closed without its validation marker", dep),
		Body:           body.String(),
		Producer:       ProducerUnvalidatedDependency,
		Standing:       true,
		Fingerprint:    "unvalidated:" + strings.Join(dependents, ","),
		Context: attention.Context{Repo: repo, Issue: number,
			Blocker: "closed without the post-merge validation marker"},
		Options: []attention.Option{
			{ID: "rerun-post-merge", Label: "Run post-merge hook",
				Verb: attention.VerbIssueRerunPostMerge, Style: attention.StylePrimary},
			noopOption("leave", "Leave held"),
		},
		DefaultAction: "leave",
		ExpiresAt:     standingExpiry(),
	}, true
}
