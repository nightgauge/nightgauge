package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestPrioritize_SkipsOpenPRBlocked: an issue whose OPEN PR is BLOCKED (a
// failing required check / branch-protection rule) must NOT be re-dispatched.
// Re-running the whole pipeline can't clear a repo-config block — only a human
// can. This guard ends the churn where a failed pr-merge reverts the issue to
// Ready and the ENTIRE pipeline re-runs against a PR that still can't merge
// (the bowlsheet #234/#244/#254/#245 pattern).
func TestPrioritize_SkipsOpenPRBlocked(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Blocked PR", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "No PR", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:               AutonomousConfig{MaxConcurrent: 5},
		state:                &AutonomousState{},
		blockedReadyPRIssues: map[string]bool{"R#1": true},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (blocked-PR issue skipped), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2 (no blocked PR), got #%d", candidates[0].Number)
	}
}

// TestPrioritize_NilBlockedSetDispatchesNormally: a nil blockedReadyPRIssues set
// (never refreshed yet, or all queries failed) reads as all-false — the guard is
// fail-open and never suppresses dispatch when we have no PR knowledge.
func TestPrioritize_NilBlockedSetDispatchesNormally(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
		// blockedReadyPRIssues intentionally nil
	}

	if got := len(as.prioritize(context.Background(), g)); got != 1 {
		t.Fatalf("nil set must not block dispatch; got %d candidates, want 1", got)
	}
}

// TestRefreshBlockedReadyPRs_MarksOnlyBlocked: the sweep records exactly the
// dispatchable, open, non-epic issues whose OPEN PR is BLOCKED. Mergeable/
// behind/dirty PRs, non-dispatchable statuses, epics, and issues with no open
// PR are all left dispatchable (unmarked). One gh pr list per repo.
func TestRefreshBlockedReadyPRs_MarksOnlyBlocked(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if !ghArgsContain(args, "list") {
			return []byte("[]"), nil
		}
		calls++
		return []byte(`[
			{"number":10,"headRefName":"feat/1-blocked","mergeStateStatus":"BLOCKED"},
			{"number":11,"headRefName":"feat/2-clean","mergeStateStatus":"CLEAN"},
			{"number":12,"headRefName":"feat/3-dirty","mergeStateStatus":"DIRTY"},
			{"number":15,"headRefName":"feat/5-blocked-but-in-review","mergeStateStatus":"BLOCKED"},
			{"number":16,"headRefName":"feat/6-blocked-epic","mergeStateStatus":"BLOCKED"}
		]`), nil
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "Ready"},                                // BLOCKED PR → marked
		"O/app#2": {Repo: "O/app", Number: 2, State: "OPEN", BoardStatus: "Ready"},                                // CLEAN PR → not marked
		"O/app#3": {Repo: "O/app", Number: 3, State: "OPEN", BoardStatus: "Ready"},                                // DIRTY PR → not marked (in-review reconcile's job)
		"O/app#4": {Repo: "O/app", Number: 4, State: "OPEN", BoardStatus: "Ready"},                                // no open PR → not marked
		"O/app#5": {Repo: "O/app", Number: 5, State: "OPEN", BoardStatus: "In review"},                            // BLOCKED PR but NOT dispatchable → not marked
		"O/app#6": {Repo: "O/app", Number: 6, State: "OPEN", BoardStatus: "Ready", Labels: []string{"type:epic"}}, // epic → not marked
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if calls != 1 {
		t.Fatalf("expected exactly 1 gh pr list for the repo, got %d", calls)
	}
	if !as.blockedReadyPRIssues["O/app#1"] {
		t.Error("expected O/app#1 (dispatchable, BLOCKED PR) to be marked")
	}
	for _, k := range []string{"O/app#2", "O/app#3", "O/app#4", "O/app#5", "O/app#6"} {
		if as.blockedReadyPRIssues[k] {
			t.Errorf("expected %s NOT marked, but it was", k)
		}
	}
}

// TestRefreshBlockedReadyPRs_GhErrorFailsOpen: when the gh query fails the sweep
// records nothing for that repo (fail-open) rather than marking issues blocked —
// dispatch is never suppressed on a transient GitHub error.
func TestRefreshBlockedReadyPRs_GhErrorFailsOpen(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"O/app#1": {Repo: "O/app", Number: 1, State: "OPEN", BoardStatus: "Ready"},
	}}

	as.refreshBlockedReadyPRs(context.Background(), g)

	if len(as.blockedReadyPRIssues) != 0 {
		t.Fatalf("gh error must leave the blocked set empty (fail-open); got %v", as.blockedReadyPRIssues)
	}
}

// --- #159: "cannot merge" is a union, not one mergeStateStatus value ---------

// TestPRCannotMergeReason_Matrix is the decision table behind the guard. GitHub
// reports exactly ONE mergeStateStatus and BEHIND outranks BLOCKED, so a PR that
// is both stale and failing a required check reports BEHIND — the state the
// pre-#159 guard read as "fine, dispatch". Each row asserts the union verdict.
func TestPRCannotMergeReason_Matrix(t *testing.T) {
	strict := branchCheckPolicy{Known: true, Strict: true, Required: map[string]bool{"ci": true}}
	loose := branchCheckPolicy{Known: true, Strict: false, Required: map[string]bool{"ci": true}}
	unknown := branchCheckPolicy{}

	tests := []struct {
		name        string
		pr          openPR
		policy      branchCheckPolicy
		wantBlocked bool
	}{
		{
			// THE REGRESSION: base moved ahead, so the same un-mergeable PR is
			// relabelled BEHIND and the BLOCKED-only guard let it through.
			name:        "behind with a failing required check is blocked",
			pr:          openPR{MergeState: "BEHIND", BaseRef: "main", FailedChecks: []string{"ci"}},
			policy:      strict,
			wantBlocked: true,
		},
		{
			name:        "behind with a failing required check is blocked even when the base is not strict",
			pr:          openPR{MergeState: "BEHIND", BaseRef: "main", FailedChecks: []string{"ci"}},
			policy:      loose,
			wantBlocked: true,
		},
		{
			name:        "behind and green is blocked when the base requires up-to-date branches",
			pr:          openPR{MergeState: "BEHIND", BaseRef: "main"},
			policy:      strict,
			wantBlocked: true,
		},
		{
			name:        "behind and green is dispatchable when the base is not strict",
			pr:          openPR{MergeState: "BEHIND", BaseRef: "main"},
			policy:      loose,
			wantBlocked: false,
		},
		{
			name:        "blocked stays blocked (pre-#159 behaviour preserved)",
			pr:          openPR{MergeState: "BLOCKED", BaseRef: "main"},
			policy:      loose,
			wantBlocked: true,
		},
		{
			name:        "blocked stays blocked without any branch-protection knowledge",
			pr:          openPR{MergeState: "BLOCKED", BaseRef: "main"},
			policy:      unknown,
			wantBlocked: true,
		},
		{
			name:        "clean is dispatchable",
			pr:          openPR{MergeState: "CLEAN", BaseRef: "main"},
			policy:      strict,
			wantBlocked: false,
		},
		{
			// pr-merge waits only on REQUIRED checks, so a red optional check
			// must not suppress dispatch.
			name:        "a failing non-required check is dispatchable",
			pr:          openPR{MergeState: "UNSTABLE", BaseRef: "main", FailedChecks: []string{"optional-lint"}},
			policy:      strict,
			wantBlocked: false,
		},
		{
			// Unreadable protection must never invent a block.
			name:        "unknown branch policy never blocks on checks or staleness",
			pr:          openPR{MergeState: "BEHIND", BaseRef: "main", FailedChecks: []string{"ci"}},
			policy:      unknown,
			wantBlocked: false,
		},
		{
			// DIRTY belongs to reconcileStuckInReviewPRs; pr-merge's rebase path
			// can clear it, so the dispatch guard must leave it alone.
			name:        "dirty is left to the in-review reconcile",
			pr:          openPR{MergeState: "DIRTY", BaseRef: "main"},
			policy:      strict,
			wantBlocked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := prCannotMergeReason(tc.pr, tc.policy)
			if got := reason != ""; got != tc.wantBlocked {
				t.Fatalf("prCannotMergeReason(%+v) = %q (blocked=%v), want blocked=%v", tc.pr, reason, got, tc.wantBlocked)
			}
		})
	}
}

// TestPRNeedsBranchPolicy_OnlyWhenTheVerdictTurnsOnIt: the quota guard. The
// branch-protection probe must fire only for PRs whose verdict actually depends
// on it — never for a CLEAN PR, and never for one already conclusively BLOCKED.
func TestPRNeedsBranchPolicy_OnlyWhenTheVerdictTurnsOnIt(t *testing.T) {
	tests := []struct {
		name string
		pr   openPR
		want bool
	}{
		{"clean and green", openPR{MergeState: "CLEAN"}, false},
		{"blocked is already conclusive", openPR{MergeState: "BLOCKED", FailedChecks: []string{"ci"}}, false},
		{"dirty and green", openPR{MergeState: "DIRTY"}, false},
		{"behind and green needs the strict flag", openPR{MergeState: "BEHIND"}, true},
		{"failing check needs the required set", openPR{MergeState: "UNSTABLE", FailedChecks: []string{"ci"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prNeedsBranchPolicy(tc.pr); got != tc.want {
				t.Fatalf("prNeedsBranchPolicy(%+v) = %v, want %v", tc.pr, got, tc.want)
			}
		})
	}
}

// TestFailedCheckNames_ClassifiesRollupRows: the rollup rides along on the same
// batched list call, so its two shapes (CheckRun and legacy StatusContext) must
// both be classified here. In-flight checks are NOT failures — treating them as
// such would suppress dispatch for every PR with CI still running.
func TestFailedCheckNames_ClassifiesRollupRows(t *testing.T) {
	rows := []checkRollupRow{
		{TypeName: "CheckRun", Name: "go-test", Status: "COMPLETED", Conclusion: "FAILURE"},
		{TypeName: "CheckRun", Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		{TypeName: "CheckRun", Name: "docs", Status: "COMPLETED", Conclusion: "SKIPPED"},
		{TypeName: "CheckRun", Name: "flaky", Status: "COMPLETED", Conclusion: "NEUTRAL"},
		{TypeName: "CheckRun", Name: "still-running", Status: "IN_PROGRESS"},
		{TypeName: "CheckRun", Name: "timed-out", Status: "COMPLETED", Conclusion: "TIMED_OUT"},
		{TypeName: "StatusContext", Context: "cla", State: "FAILURE"},
		{TypeName: "StatusContext", Context: "legacy-ok", State: "SUCCESS"},
		{TypeName: "StatusContext", Context: "legacy-pending", State: "PENDING"},
	}

	got := failedCheckNames(rows)
	want := []string{"go-test", "timed-out", "cla"}
	if len(got) != len(want) {
		t.Fatalf("failedCheckNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("failedCheckNames = %v, want %v", got, want)
		}
	}
}

// blockedPRGhStub serves the two calls the sweep can make: the batched
// `gh pr list` (open PRs + merge state + check rollup) and the per-base-branch
// rules probe. It counts each so the tests can assert the quota contract.
type blockedPRGhStub struct {
	prListJSON  string
	rulesJSON   string
	rulesErr    error
	listCalls   int
	policyCalls int
}

func (s *blockedPRGhStub) exec(_ context.Context, args ...string) ([]byte, error) {
	if ghArgsContain(args, "api") {
		s.policyCalls++
		if s.rulesErr != nil {
			return nil, s.rulesErr
		}
		return []byte(s.rulesJSON), nil
	}
	s.listCalls++
	return []byte(s.prListJSON), nil
}

// strictRulesJSON is a rulesets response requiring "ci" with the up-to-date
// (strict) policy set to want.
func strictRulesJSON(strict bool) string {
	return fmt.Sprintf(`[
		{"type":"pull_request","parameters":{"required_approving_review_count":1}},
		{"type":"required_status_checks","parameters":{
			"strict_required_status_checks_policy":%t,
			"required_status_checks":[{"context":"ci"}]
		}}
	]`, strict)
}

func readyGraph(numbers ...int) *depgraph.Graph {
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{}}
	for _, n := range numbers {
		g.Nodes[fmt.Sprintf("O/app#%d", n)] = &depgraph.Node{
			Repo: "O/app", Number: n, State: "OPEN", BoardStatus: "Ready",
		}
	}
	return g
}

// TestRefreshBlockedReadyPRs_UnionMatrix drives the full #159 matrix through the
// real sweep — batched list call included — so the wiring (rollup fields on the
// same list call, lazily-resolved branch policy) is covered, not just the pure
// predicate.
func TestRefreshBlockedReadyPRs_UnionMatrix(t *testing.T) {
	const failingCI = `"statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]`
	const greenCI = `"statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS"}]`

	tests := []struct {
		name        string
		prJSON      string
		strict      bool
		wantBlocked bool
	}{
		{
			name:        "behind with a failing required check",
			prJSON:      `{"number":10,"headRefName":"feat/1-x","baseRefName":"main","mergeStateStatus":"BEHIND",` + failingCI + `}`,
			strict:      true,
			wantBlocked: true,
		},
		{
			name:        "behind and green under a strict base",
			prJSON:      `{"number":10,"headRefName":"feat/1-x","baseRefName":"main","mergeStateStatus":"BEHIND",` + greenCI + `}`,
			strict:      true,
			wantBlocked: true,
		},
		{
			name:        "behind and green under a non-strict base",
			prJSON:      `{"number":10,"headRefName":"feat/1-x","baseRefName":"main","mergeStateStatus":"BEHIND",` + greenCI + `}`,
			strict:      false,
			wantBlocked: false,
		},
		{
			name:        "blocked",
			prJSON:      `{"number":10,"headRefName":"feat/1-x","baseRefName":"main","mergeStateStatus":"BLOCKED",` + failingCI + `}`,
			strict:      false,
			wantBlocked: true,
		},
		{
			name:        "clean",
			prJSON:      `{"number":10,"headRefName":"feat/1-x","baseRefName":"main","mergeStateStatus":"CLEAN",` + greenCI + `}`,
			strict:      true,
			wantBlocked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &blockedPRGhStub{
				prListJSON: "[" + tc.prJSON + "]",
				rulesJSON:  strictRulesJSON(tc.strict),
			}
			stubReconcileGh(t, stub.exec)

			as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
			as.refreshBlockedReadyPRs(context.Background(), readyGraph(1))

			if got := as.blockedReadyPRIssues["O/app#1"]; got != tc.wantBlocked {
				t.Fatalf("blocked=%v, want %v (set=%v)", got, tc.wantBlocked, as.blockedReadyPRIssues)
			}
			if stub.listCalls != 1 {
				t.Fatalf("expected exactly 1 batched gh pr list, got %d", stub.listCalls)
			}
		})
	}
}

// TestRefreshBlockedReadyPRs_OneListAndOnePolicyCallPerRepo is the quota
// contract (#159 hard constraint). The check rollup must ride along on the
// single batched list call, and the branch policy must be resolved once per
// base branch — never once per PR, no matter how many candidates share the repo.
func TestRefreshBlockedReadyPRs_OneListAndOnePolicyCallPerRepo(t *testing.T) {
	stub := &blockedPRGhStub{
		prListJSON: `[
			{"number":10,"headRefName":"feat/1-a","baseRefName":"main","mergeStateStatus":"BEHIND",
			 "statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]},
			{"number":11,"headRefName":"feat/2-b","baseRefName":"main","mergeStateStatus":"BEHIND",
			 "statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]},
			{"number":12,"headRefName":"feat/3-c","baseRefName":"main","mergeStateStatus":"BEHIND",
			 "statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS"}]}
		]`,
		rulesJSON: strictRulesJSON(true),
	}
	stubReconcileGh(t, stub.exec)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.refreshBlockedReadyPRs(context.Background(), readyGraph(1, 2, 3))

	for _, key := range []string{"O/app#1", "O/app#2", "O/app#3"} {
		if !as.blockedReadyPRIssues[key] {
			t.Errorf("expected %s blocked, set=%v", key, as.blockedReadyPRIssues)
		}
	}
	if stub.listCalls != 1 {
		t.Fatalf("expected exactly 1 batched gh pr list, got %d", stub.listCalls)
	}
	if stub.policyCalls != 1 {
		t.Fatalf("expected exactly 1 branch-policy lookup for base main, got %d", stub.policyCalls)
	}
}

// TestRefreshBlockedReadyPRs_CleanPRsSkipTheBranchPolicyLookup: a repo whose
// open PRs are all mergeable must pay nothing beyond the batched list call.
func TestRefreshBlockedReadyPRs_CleanPRsSkipTheBranchPolicyLookup(t *testing.T) {
	stub := &blockedPRGhStub{
		prListJSON: `[{"number":10,"headRefName":"feat/1-a","baseRefName":"main","mergeStateStatus":"CLEAN",
			"statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS"}]}]`,
		rulesJSON: strictRulesJSON(true),
	}
	stubReconcileGh(t, stub.exec)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.refreshBlockedReadyPRs(context.Background(), readyGraph(1))

	if stub.policyCalls != 0 {
		t.Fatalf("a CLEAN PR must not trigger a branch-policy lookup; got %d", stub.policyCalls)
	}
	if as.blockedReadyPRIssues["O/app#1"] {
		t.Fatal("CLEAN PR must stay dispatchable")
	}
}

// TestRefreshBlockedReadyPRs_BranchPolicyErrorFailsOpen: when the protection
// probe fails (no scope, transient error) the guard degrades to its pre-#159
// BLOCKED-only behaviour instead of inventing a block. Fail-open is preserved
// for BOTH queries, not just the list call.
func TestRefreshBlockedReadyPRs_BranchPolicyErrorFailsOpen(t *testing.T) {
	stub := &blockedPRGhStub{
		prListJSON: `[{"number":10,"headRefName":"feat/1-a","baseRefName":"main","mergeStateStatus":"BEHIND",
			"statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]}]`,
		rulesErr: context.DeadlineExceeded,
	}
	stubReconcileGh(t, stub.exec)

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	as.refreshBlockedReadyPRs(context.Background(), readyGraph(1))

	if as.blockedReadyPRIssues["O/app#1"] {
		t.Fatalf("unreadable branch protection must not block dispatch; set=%v", as.blockedReadyPRIssues)
	}
}

// TestFetchBranchCheckPolicy_FallsBackToClassicProtection: rulesets are probed
// first (no administration:read needed), classic branch protection second —
// the same two-source union internal/github uses for required check names. A
// repo on classic protection must still yield strict + required contexts.
func TestFetchBranchCheckPolicy_FallsBackToClassicProtection(t *testing.T) {
	var paths []string
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		paths = append(paths, path)
		if strings.Contains(path, "/rules/branches/") {
			return []byte(`[{"type":"pull_request","parameters":{}}]`), nil
		}
		return []byte(`{"strict":true,"contexts":["ci","lint"]}`), nil
	})

	policy := fetchBranchCheckPolicy(context.Background(), "O/app", "main")

	if !policy.Known || !policy.Strict {
		t.Fatalf("expected a known, strict policy from classic protection; got %+v", policy)
	}
	if !policy.Required["ci"] || !policy.Required["lint"] {
		t.Fatalf("expected required contexts ci+lint; got %+v", policy.Required)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "/rules/branches/") {
		t.Fatalf("expected rulesets probed before classic protection; got %v", paths)
	}
}

// TestFetchBranchCheckPolicy_RulesetsShortCircuitClassic: when rulesets answer,
// the classic endpoint is not probed at all — one call, not two.
func TestFetchBranchCheckPolicy_RulesetsShortCircuitClassic(t *testing.T) {
	calls := 0
	stubReconcileGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		return []byte(strictRulesJSON(true)), nil
	})

	policy := fetchBranchCheckPolicy(context.Background(), "O/app", "main")

	if !policy.Known || !policy.Strict || !policy.Required["ci"] {
		t.Fatalf("expected strict policy requiring ci; got %+v", policy)
	}
	if calls != 1 {
		t.Fatalf("rulesets answered — expected exactly 1 probe, got %d", calls)
	}
}

// TestIsWellFormedBranch guards the ref before it is interpolated into a GitHub
// API path.
func TestIsWellFormedBranch(t *testing.T) {
	for _, ok := range []string{"main", "release/1.2", "feat/159-guard"} {
		if !isWellFormedBranch(ok) {
			t.Errorf("expected %q to be well formed", ok)
		}
	}
	for _, bad := range []string{"", "/main", "../../etc", "main branch", "main?x=1", "a#b", "a%2f"} {
		if isWellFormedBranch(bad) {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}
