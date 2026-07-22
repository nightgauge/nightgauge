package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// maxInReviewRecoveryAttempts bounds how many times the scheduler will re-run
// pr-merge for a single stuck-in-review issue before leaving it for human
// triage. A genuinely unresolvable conflict must not loop forever.
const maxInReviewRecoveryAttempts = 2

// reconcileStuckInReviewPRs detects issues parked in board status "In review"
// whose PR is OPEN but cannot merge as-is — mergeStateStatus BEHIND (base moved
// ahead) or DIRTY (content conflict). This is the "a PR left sitting" deadlock:
// pr-merge failed (or never completed), the issue went to "In review", and then
// a sibling PR advanced the base — so the branch is now stale/conflicting.
//
// Such an issue is invisible to normal scheduling: isWorkCompleteStatus treats
// "In review" as done (so it never blocks), and isDispatchableStatus rejects it
// (so it's never retried). It therefore sits forever while keeping its parent
// epic open, silently deadlocking every downstream wave that blocks on that
// epic. See #3894.
//
// Recovery reuses the normal flow: move the issue back to "Ready" so the next
// dispatch re-runs pr-merge, whose existing rebase-on-conflict path freshens the
// branch and AI-resolves the conflict, then merges. Bounded per issue by
// maxInReviewRecoveryAttempts so an unresolvable conflict surfaces loudly for
// human triage instead of looping.
//
// Fail-closed: only a verified OPEN PR with a BEHIND/DIRTY merge state triggers
// a move. A clean, mergeable in-review PR (legitimately awaiting the merge
// stage) is never touched. Any gh/board error skips the issue.
func (as *AutonomousScheduler) reconcileStuckInReviewPRs(ctx context.Context, graph *depgraph.Graph) {
	if graph == nil {
		return
	}

	// First pass: collect the in-review, non-epic, OPEN candidates grouped by
	// repo. Then query each repo's open PRs exactly ONCE (#3896) — the prior
	// per-node lookup issued a gh-pr-list per candidate every cycle, draining
	// the GitHub quota the pipeline-start preflight depends on.
	candidates := map[string][]*depgraph.Node{}
	for _, node := range graph.Nodes {
		if node == nil || !strings.EqualFold(node.State, "OPEN") {
			continue
		}
		// Only issues parked in "In review"; epics and other statuses are out.
		if !isWorkCompleteStatus(node.BoardStatus) || nodeHasEpicLabel(node) {
			continue
		}
		candidates[node.Repo] = append(candidates[node.Repo], node)
	}

	for repo, nodes := range candidates {
		mergeStates, ok := as.openPRMergeStatesForRepo(ctx, repo)
		if !ok {
			continue // query failed — leave this repo's items alone
		}
		for _, node := range nodes {
			mergeState, found := mergeStates[node.Number]
			if !found {
				continue // no open PR for this issue — leave it alone
			}
			if !strings.EqualFold(mergeState, "BEHIND") && !strings.EqualFold(mergeState, "DIRTY") {
				continue // CLEAN/BLOCKED/UNSTABLE/etc — not the stale/conflict case
			}

			key := fmt.Sprintf("%s#%d", node.Repo, node.Number)
			as.mu.Lock()
			if as.inReviewRecoveryAttempts == nil {
				as.inReviewRecoveryAttempts = map[string]int{}
			}
			attempts := as.inReviewRecoveryAttempts[key]
			as.mu.Unlock()

			if attempts >= maxInReviewRecoveryAttempts {
				log.Printf("autonomous: stuck-in-review: %s PR is %s after %d recovery attempt(s) — leaving for human triage (resolve the conflict and merge manually)",
					key, mergeState, attempts)
				continue
			}

			owner, repoName := splitOwnerRepo(node.Repo)
			projectNum, ownerType := as.projectForRepo(owner, repoName)
			if projectNum == 0 {
				log.Printf("autonomous: stuck-in-review: no project config for %s — skipping #%d", node.Repo, node.Number)
				continue
			}

			projSvc := gh.NewProjectService(as.ghClient, owner, projectNum, ownerType)
			if err := projSvc.MoveStatus(ctx, owner, repoName, node.Number, "Ready"); err != nil {
				log.Printf("autonomous: stuck-in-review: failed to move %s In review → Ready: %v", key, err)
				continue
			}

			// Reflect the move in the cached graph so (a) this same cycle's
			// candidate selection can pick it up immediately, and (b) subsequent
			// cached cycles don't re-detect and re-move it before the next fresh
			// build.
			node.BoardStatus = "Ready"

			as.mu.Lock()
			as.inReviewRecoveryAttempts[key] = attempts + 1
			as.mu.Unlock()

			log.Printf("autonomous: stuck-in-review: %s PR is %s (cannot merge as-is) — moved In review → Ready to re-run pr-merge (attempt %d/%d)",
				key, mergeState, attempts+1, maxInReviewRecoveryAttempts)
		}
	}
}

// refreshBlockedReadyPRs recomputes the set of dispatchable (Ready/Backlog)
// issues whose OPEN PR is BLOCKED — a failing REQUIRED status check or a
// branch-protection rule that no amount of pipeline retry can clear; only a
// human can (fix the failing check, or correct the required-checks config). It
// stores the set on the scheduler so prioritize() skips re-dispatching those
// issues, ending the re-run churn where a failed pr-merge reverts the issue to
// Ready and the WHOLE pipeline runs again against a PR that still can't merge
// (the bowlsheet #234/#244/#254/#245 pattern: many full re-runs, each failing
// at pr-merge because a required check is red).
//
// Like reconcileStuckInReviewPRs this makes one gh-pr-list call per repo, so the
// caller gates it to FRESH graph builds (the graph TTL cadence) to protect the
// shared GitHub quota the pipeline-start preflight depends on. The set is
// REPLACED wholesale each refresh: a repo whose query fails simply contributes
// nothing (fail-open — prioritize falls back to normal dispatch, never worse
// than before this guard existed), and once a PR unblocks, merges, or closes it
// drops out of the set on the next fresh scan. Non-destructive: board status is
// never touched, so nothing can be parked or deadlocked by this sweep.
func (as *AutonomousScheduler) refreshBlockedReadyPRs(ctx context.Context, graph *depgraph.Graph) {
	if graph == nil {
		return
	}

	// Group dispatchable, open, non-epic candidates by repo so each repo's open
	// PRs are listed exactly once (mirrors #3896 quota discipline).
	candidates := map[string][]*depgraph.Node{}
	for _, node := range graph.Nodes {
		if node == nil || !strings.EqualFold(node.State, "OPEN") {
			continue
		}
		if nodeHasEpicLabel(node) {
			continue
		}
		if !isDispatchableStatus(node.BoardStatus, as.config.PickupBacklog) {
			continue
		}
		candidates[node.Repo] = append(candidates[node.Repo], node)
	}

	blocked := map[string]bool{}
	for repo, nodes := range candidates {
		mergeStates, ok := as.openPRMergeStatesForRepo(ctx, repo)
		if !ok {
			continue // query failed — leave this repo out (fail-open)
		}
		for _, node := range nodes {
			state, found := mergeStates[node.Number]
			if !found || !strings.EqualFold(state, "BLOCKED") {
				continue // no open PR, or PR is mergeable/behind/dirty — not our case
			}
			key := fmt.Sprintf("%s#%d", node.Repo, node.Number)
			blocked[key] = true
			log.Printf("autonomous: %s has an OPEN PR that is BLOCKED (failing required check / branch protection) — will not re-dispatch; needs human, no retry can clear", key)
		}
	}

	as.mu.Lock()
	as.blockedReadyPRIssues = blocked
	as.mu.Unlock()
}

// nodeHasEpicLabel reports whether the node carries the type:epic label (epics
// are tracked, not dispatched — mirrors the candidate-selection check).
func nodeHasEpicLabel(node *depgraph.Node) bool {
	for _, label := range node.Labels {
		if strings.EqualFold(label, "type:epic") {
			return true
		}
	}
	return false
}

// openPRMergeStatesForRepo lists a repo's OPEN PRs in a SINGLE gh call and
// returns a map of issue-number → mergeStateStatus, keyed by the issue parsed
// from each PR's head branch (feat/<n>-… convention). ok is false only when the
// query itself fails; an empty map (repo has no open PRs) is a valid ok=true
// result. Batching per repo — rather than one list per candidate — is what
// keeps the in-review reconcile sweep cheap on the GitHub quota (#3896).
func (as *AutonomousScheduler) openPRMergeStatesForRepo(ctx context.Context, repo string) (map[int]string, bool) {
	if !isWellFormedRepo(repo) {
		return nil, false
	}
	out, err := reconcileExecGh(ctx, "pr", "list", "--repo", repo, "--state", "open",
		"--json", "number,headRefName,mergeStateStatus", "--limit", "100")
	if err != nil {
		return nil, false
	}
	var prs []struct {
		Number           int    `json:"number"`
		HeadRefName      string `json:"headRefName"`
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if jsonErr := json.Unmarshal(out, &prs); jsonErr != nil {
		return nil, false
	}
	states := make(map[int]string, len(prs))
	for _, pr := range prs {
		if n, ok := gitpkg.ParseIssueNumberFromBranch(pr.HeadRefName); ok {
			states[n] = pr.MergeStateStatus
		}
	}
	return states, true
}

// openPRMergeStateForIssue returns the mergeStateStatus of the OPEN PR whose
// head branch belongs to the given issue. Thin wrapper over the batched
// openPRMergeStatesForRepo. Returns ("", false) when no open PR matches or the
// query fails — callers treat that as "nothing to recover".
func (as *AutonomousScheduler) openPRMergeStateForIssue(ctx context.Context, repo string, number int) (string, bool) {
	if number <= 0 {
		return "", false
	}
	states, ok := as.openPRMergeStatesForRepo(ctx, repo)
	if !ok {
		return "", false
	}
	state, found := states[number]
	return state, found
}

// projectForRepo resolves the GitHub project number and owner type for a repo
// from the scheduler's configured repos. Mirrors the lookup in
// recoverOrphanedRunning. Returns (0, "") when the repo isn't configured.
func (as *AutonomousScheduler) projectForRepo(owner, repoName string) (int, gh.OwnerType) {
	for _, rc := range as.repos {
		if rc.Owner == owner && rc.Name == repoName && rc.Project > 0 {
			return rc.Project, rc.OwnerType
		}
	}
	return 0, ""
}
