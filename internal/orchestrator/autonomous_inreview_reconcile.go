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
		openPRs, ok := as.openPRMergeStatesForRepo(ctx, repo)
		if !ok {
			continue // query failed — leave this repo's items alone
		}
		for _, node := range nodes {
			pr, found := openPRs[node.Number]
			if !found {
				continue // no open PR for this issue — leave it alone
			}
			mergeState := pr.MergeState
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
// issues whose OPEN PR cannot merge as-is — a failing REQUIRED status check, a
// branch-protection rule, or a stale head under a strict up-to-date policy. No
// amount of pipeline retry clears those; only a human can (fix the failing
// check, or correct the required-checks config). It stores the set on the
// scheduler so prioritize() skips re-dispatching those issues, ending the
// re-run churn where a failed pr-merge reverts the issue to Ready and the WHOLE
// pipeline runs again against a PR that still can't merge (the bowlsheet
// #234/#244/#254/#245 pattern: many full re-runs, each failing at pr-merge
// because a required check is red).
//
// "Cannot merge" is a UNION of conditions, not one enum value (#159). GitHub
// reports a SINGLE mergeStateStatus and BEHIND outranks BLOCKED, so a PR that
// is both out-of-date with base AND failing a required check reports BEHIND —
// keying the guard on BLOCKED alone let exactly that PR through and re-ran the
// whole pipeline against it. See prCannotMergeReason for the union.
//
// Like reconcileStuckInReviewPRs this makes one gh-pr-list call per repo, so the
// caller gates it to FRESH graph builds (the graph TTL cadence) to protect the
// shared GitHub quota the pipeline-start preflight depends on. The check rollup
// rides along in that SAME list call (no per-PR round-trip), and the branch
// policy is resolved at most once per repo+base branch and only when the answer
// actually depends on it. The set is REPLACED wholesale each refresh: a repo
// whose query fails simply contributes nothing (fail-open — prioritize falls
// back to normal dispatch, never worse than before this guard existed), and
// once a PR unblocks, merges, or closes it drops out of the set on the next
// fresh scan. Non-destructive: board status is never touched, so nothing can be
// parked or deadlocked by this sweep.
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
		openPRs, ok := as.openPRMergeStatesForRepo(ctx, repo)
		if !ok {
			continue // query failed — leave this repo out (fail-open)
		}
		policies := newBranchPolicyCache(repo)
		for _, node := range nodes {
			pr, found := openPRs[node.Number]
			if !found {
				continue // no open PR for this issue — not our case
			}
			// Resolve branch protection only when the verdict actually turns on
			// it (a failing check that may or may not be required, or a BEHIND
			// head). CLEAN/DIRTY/BLOCKED PRs are decided from the list call
			// alone, so a repo with nothing pending pays nothing extra.
			var policy branchCheckPolicy
			if prNeedsBranchPolicy(pr) {
				policy = policies.get(ctx, pr.BaseRef)
			}
			reason := prCannotMergeReason(pr, policy)
			if reason == "" {
				continue // mergeable, or only retry-clearable problems — dispatch it
			}
			key := fmt.Sprintf("%s#%d", node.Repo, node.Number)
			blocked[key] = true
			log.Printf("autonomous: %s has an OPEN PR #%d that cannot merge (%s) — will not re-dispatch; needs human, no retry can clear", key, pr.Number, reason)
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

// openPR is the mergeability snapshot of one OPEN pull request, as returned by
// the single batched list call. Everything the sweeps reason over comes from
// that one response — mergeStateStatus is not sufficient on its own because
// GitHub collapses several independent conditions into a single enum value
// (#159).
type openPR struct {
	Number int
	// MergeState is GitHub's mergeStateStatus (CLEAN/BEHIND/BLOCKED/DIRTY/…).
	MergeState string
	// BaseRef is the PR's target branch — the branch whose protection policy
	// decides whether a stale head or a red check actually blocks the merge.
	BaseRef string
	// FailedChecks are the rollup entries that reached a terminal non-passing
	// verdict. Pending/queued/in-progress entries are excluded: an in-flight
	// run says nothing about mergeability.
	FailedChecks []string
}

// openPRMergeStatesForRepo lists a repo's OPEN PRs in a SINGLE gh call and
// returns a map of issue-number → openPR, keyed by the issue parsed from each
// PR's head branch (feat/<n>-… convention). ok is false only when the query
// itself fails; an empty map (repo has no open PRs) is a valid ok=true result.
// Batching per repo — rather than one list per candidate — is what keeps the
// in-review reconcile sweep cheap on the GitHub quota (#3896).
//
// baseRefName and statusCheckRollup are selected in that same list call. They
// are extra FIELDS on a GraphQL query already being paid for, not extra
// round-trips, so the per-repo call budget is unchanged (#159).
func (as *AutonomousScheduler) openPRMergeStatesForRepo(ctx context.Context, repo string) (map[int]openPR, bool) {
	if !isWellFormedRepo(repo) {
		return nil, false
	}
	out, err := reconcileExecGh(ctx, "pr", "list", "--repo", repo, "--state", "open",
		"--json", "number,headRefName,baseRefName,mergeStateStatus,statusCheckRollup", "--limit", "100")
	if err != nil {
		return nil, false
	}
	var prs []struct {
		Number            int              `json:"number"`
		HeadRefName       string           `json:"headRefName"`
		BaseRefName       string           `json:"baseRefName"`
		MergeStateStatus  string           `json:"mergeStateStatus"`
		StatusCheckRollup []checkRollupRow `json:"statusCheckRollup"`
	}
	if jsonErr := json.Unmarshal(out, &prs); jsonErr != nil {
		return nil, false
	}
	states := make(map[int]openPR, len(prs))
	for _, pr := range prs {
		n, ok := gitpkg.ParseIssueNumberFromBranch(pr.HeadRefName)
		if !ok {
			continue
		}
		states[n] = openPR{
			Number:       pr.Number,
			MergeState:   pr.MergeStateStatus,
			BaseRef:      pr.BaseRefName,
			FailedChecks: failedCheckNames(pr.StatusCheckRollup),
		}
	}
	return states, true
}

// checkRollupRow is one entry of `gh pr list --json statusCheckRollup`. The
// rollup is heterogeneous: CheckRun rows carry name/status/conclusion, legacy
// StatusContext rows carry context/state. Both shapes decode into this one
// struct; only the fields present for that __typename are populated.
type checkRollupRow struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// passingCheckConclusions are the terminal CheckRun conclusions that do NOT
// block a merge (mirrors internal/github's classification).
var passingCheckConclusions = map[string]bool{
	"SUCCESS": true,
	"NEUTRAL": true,
	"SKIPPED": true,
}

// failedCheckNames returns the names of rollup entries that have reached a
// terminal, non-passing verdict. A check still running (CheckRun status !=
// COMPLETED, or a StatusContext state of PENDING/EXPECTED) is NOT a failure —
// treating it as one would suppress dispatch for every PR with CI in flight.
func failedCheckNames(rows []checkRollupRow) []string {
	var failed []string
	for _, row := range rows {
		name := row.Name
		if name == "" {
			name = row.Context
		}
		if name == "" {
			continue
		}
		if strings.EqualFold(row.TypeName, "StatusContext") {
			if strings.EqualFold(row.State, "FAILURE") || strings.EqualFold(row.State, "ERROR") {
				failed = append(failed, name)
			}
			continue
		}
		// CheckRun (the default shape): only a COMPLETED run has a verdict.
		if !strings.EqualFold(row.Status, "COMPLETED") {
			continue
		}
		if !passingCheckConclusions[strings.ToUpper(row.Conclusion)] {
			failed = append(failed, name)
		}
	}
	return failed
}

// branchCheckPolicy is the merge-relevant slice of a base branch's protection:
// which status checks are REQUIRED, and whether the branch must be up to date
// with base before merging ("strict"). Known is false when no protection source
// answered — the guard then declines to draw any conclusion that depends on
// protection, which keeps it fail-open.
type branchCheckPolicy struct {
	Known    bool
	Strict   bool
	Required map[string]bool
}

// prNeedsBranchPolicy reports whether prCannotMergeReason's verdict for this PR
// depends on branch protection. Keeping it separate lets the caller skip the
// protection lookup entirely for PRs decided by the list call alone.
func prNeedsBranchPolicy(pr openPR) bool {
	if strings.EqualFold(pr.MergeState, "BLOCKED") {
		return false // already conclusive
	}
	return len(pr.FailedChecks) > 0 || strings.EqualFold(pr.MergeState, "BEHIND")
}

// prCannotMergeReason returns why an OPEN PR cannot merge without a human, or
// "" when the pipeline should still be allowed to dispatch against it.
//
// "Cannot merge" is a UNION, because GitHub reports exactly ONE mergeStateStatus
// and BEHIND outranks BLOCKED (#159):
//
//   - BLOCKED — a required check is red or a protection rule is unsatisfied.
//   - a failing REQUIRED check — disqualifying at ANY mergeStateStatus. This is
//     the case the old BLOCKED-only guard missed: as soon as base moved ahead,
//     the same un-mergeable PR was relabelled BEHIND and let through.
//   - BEHIND under a strict up-to-date policy — the head is stale and the base
//     branch refuses stale heads, so the PR is un-mergeable by the repo's own
//     rules.
//
// DIRTY (content conflict) is deliberately NOT here: reconcileStuckInReviewPRs
// owns that case and pr-merge's rebase path can clear it.
//
// A red check that is NOT required is likewise not disqualifying — pr-merge
// waits only on required checks — which is why the required set matters and an
// unknown policy (Known=false) never blocks: no protection knowledge, no
// suppression.
func prCannotMergeReason(pr openPR, policy branchCheckPolicy) string {
	if strings.EqualFold(pr.MergeState, "BLOCKED") {
		return "mergeStateStatus=BLOCKED — failing required check / branch protection"
	}
	if failing := failingRequiredChecks(pr.FailedChecks, policy); len(failing) > 0 {
		return fmt.Sprintf("required check(s) failing: %s (mergeStateStatus=%s)",
			strings.Join(failing, ", "), pr.MergeState)
	}
	if strings.EqualFold(pr.MergeState, "BEHIND") && policy.Known && policy.Strict {
		return fmt.Sprintf("mergeStateStatus=BEHIND and %s requires branches be up to date before merging", pr.BaseRef)
	}
	return ""
}

// failingRequiredChecks intersects a PR's failed checks with the base branch's
// required set. An unknown policy yields nothing: without protection knowledge
// the guard must not suppress dispatch.
func failingRequiredChecks(failed []string, policy branchCheckPolicy) []string {
	if !policy.Known || len(policy.Required) == 0 {
		return nil
	}
	var required []string
	for _, name := range failed {
		if policy.Required[name] {
			required = append(required, name)
		}
	}
	return required
}

// branchPolicyCache memoises branch protection lookups for the life of one
// sweep. Keyed by base branch, so a repo whose PRs all target main pays for at
// most one lookup — and only when prNeedsBranchPolicy says the verdict turns on
// it. Never per PR (#159 quota constraint).
type branchPolicyCache struct {
	repo  string
	byRef map[string]branchCheckPolicy
}

func newBranchPolicyCache(repo string) *branchPolicyCache {
	return &branchPolicyCache{repo: repo, byRef: map[string]branchCheckPolicy{}}
}

func (c *branchPolicyCache) get(ctx context.Context, baseRef string) branchCheckPolicy {
	if policy, ok := c.byRef[baseRef]; ok {
		return policy
	}
	policy := fetchBranchCheckPolicy(ctx, c.repo, baseRef)
	c.byRef[baseRef] = policy
	return policy
}

// fetchBranchCheckPolicy reads the required-check contexts and the strict
// up-to-date flag for a base branch. Rulesets are consulted first — they need
// no administration:read scope and are where modern protection lives — and
// classic branch protection is the fallback, mirroring the two-source union in
// internal/github's GetRequiredCheckNames (a ruleset-enforced required check is
// invisible to the protection endpoint, and vice versa).
//
// Any failure (missing scope, 404 "Branch not protected", malformed body)
// returns Known=false, which makes every protection-dependent branch of
// prCannotMergeReason inert — the guard degrades to exactly its pre-#159
// BLOCKED-only behaviour rather than suppressing dispatch on a bad read.
func fetchBranchCheckPolicy(ctx context.Context, repo, baseRef string) branchCheckPolicy {
	policy := branchCheckPolicy{Required: map[string]bool{}}
	if !isWellFormedRepo(repo) || !isWellFormedBranch(baseRef) {
		return policy
	}

	if out, err := reconcileExecGh(ctx, "api", fmt.Sprintf("repos/%s/rules/branches/%s", repo, baseRef)); err == nil {
		var rules []struct {
			Type       string `json:"type"`
			Parameters struct {
				Strict   bool `json:"strict_required_status_checks_policy"`
				Required []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			} `json:"parameters"`
		}
		if json.Unmarshal(out, &rules) == nil {
			for _, rule := range rules {
				if !strings.EqualFold(rule.Type, "required_status_checks") {
					continue
				}
				policy.Known = true
				if rule.Parameters.Strict {
					policy.Strict = true
				}
				for _, check := range rule.Parameters.Required {
					if check.Context != "" {
						policy.Required[check.Context] = true
					}
				}
			}
		}
	}
	if policy.Known {
		return policy
	}

	if out, err := reconcileExecGh(ctx, "api", fmt.Sprintf("repos/%s/branches/%s/protection/required_status_checks", repo, baseRef)); err == nil {
		var classic struct {
			Strict   bool     `json:"strict"`
			Contexts []string `json:"contexts"`
		}
		if json.Unmarshal(out, &classic) == nil {
			policy.Known = true
			policy.Strict = classic.Strict
			for _, name := range classic.Contexts {
				if name != "" {
					policy.Required[name] = true
				}
			}
		}
	}
	return policy
}

// isWellFormedBranch guards a branch name before it is interpolated into a
// GitHub API path. exec (argv, no shell) already prevents metacharacter
// injection; this rejects path-traversal and whitespace so a malformed ref
// fails closed rather than producing a bogus request.
func isWellFormedBranch(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "/") || strings.Contains(branch, "..") {
		return false
	}
	for _, r := range branch {
		if r <= ' ' || r == '?' || r == '#' || r == '%' || r == '&' {
			return false
		}
	}
	return true
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
	pr, found := states[number]
	return pr.MergeState, found
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
