package attention

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryIsClosedAllowlist(t *testing.T) {
	registered := []string{
		VerbQueueAdd, VerbIssueRemoveBlockedBy, VerbAutonomousResume, VerbAutonomousRescan,
		VerbAutonomousComplete, VerbAutonomousClearIssueFailures, VerbProjectSyncStatus,
		VerbIssueClose, VerbBudgetRaiseCeiling, VerbRunRetryWithEscalation,
		VerbIssueApproveArchitecture, VerbDependabotEnableAlerts,
		VerbWorkspaceAddRepo, VerbBlockedFindingClear, VerbPRUpdateBranch, VerbNoop,
	}
	for _, v := range registered {
		if !IsRegisteredVerb(v) {
			t.Errorf("verb %q should be registered", v)
		}
	}
	// The two new verbs the fleet lacked before E1 must be present.
	if !IsRegisteredVerb(VerbBudgetRaiseCeiling) || !IsRegisteredVerb(VerbRunRetryWithEscalation) {
		t.Error("the two new E1 verbs must be registered")
	}
	// The architecture-approval verb is the first that mutates labels; it must
	// be registry-gated like any other, never special-cased.
	if !IsRegisteredVerb(VerbIssueApproveArchitecture) {
		t.Error("issue.approveArchitecture must be registered")
	}
	// The dependabot-coverage card's repair button (#344) — the first verb that
	// changes a repository SETTING, and allowed one only because the condition
	// it repairs is a single deterministic flip on a repo config already covers.
	if !IsRegisteredVerb(VerbDependabotEnableAlerts) {
		t.Error("dependabot.enableAlerts must be registered")
	}
	// The coverage-gap card's repair button (#706), allowed once #703 gave the
	// workspace manifest a deterministic writer.
	if !IsRegisteredVerb(VerbWorkspaceAddRepo) {
		t.Error("workspace.addRepo must be registered")
	}
	// The out-of-scope-blocker card's clear button (#1147). It is the only
	// retraction for a hold that otherwise defers an issue at pickup forever,
	// so a card without it would be the dead end Invariant 3 forbids.
	if !IsRegisteredVerb(VerbBlockedFindingClear) {
		t.Error("blocked.clearFinding must be registered")
	}
	// The human-gate `behind` card's repair button (#1575). The producer's own
	// comment used to defend its absence — "no verb in the registry can approve
	// a PR or rebase a branch" — which was an argument for adding the verb, not
	// for interrupting a person.
	if !IsRegisteredVerb(VerbPRUpdateBranch) {
		t.Error("pr.updateBranch must be registered")
	}
	// Anything not on the allowlist is rejected — the security boundary. The
	// near-miss spellings of the approval verb must NOT resolve: the executor
	// resolves the label name from config, so a surface cannot reach a
	// generic label-mutating verb even by guessing a plausible name. The same
	// holds for the settings verb: nothing generic ("repo.updateSettings") and
	// nothing adjacent ("dependabot.disableAlerts") is reachable by guessing.
	for _, v := range []string{"rm", "shell.exec", "queue.remove", "", "budget.raise",
		"issue.addLabel", "issue.approveArchitecture ", "issue.approve",
		"dependabot.enableAlerts ", "dependabot.enable", "dependabot.disableAlerts",
		"repo.updateSettings", "security.enable",
		// Nothing generic or adjacent to the manifest writer is reachable by
		// guessing: the card can add a repo and nothing else.
		"workspace.addRepo ", "workspace.removeRepo", "workspace.write",
		"workspace.setRouting", "manifest.write",
		// Nothing generic or adjacent to the finding clearer is reachable by
		// guessing: the card can retract one issue's hold and nothing else.
		"blocked.clearFinding ", "blocked.clear", "blocked.writeFinding",
		"pipeline.deleteFile", "findings.clear",
		// Nothing generic or adjacent to the branch updater is reachable by
		// guessing: the card can bring one PR up to date with its base and
		// nothing else. Merging, approving and force-pushing stay unreachable.
		"pr.updateBranch ", "pr.update", "pr.merge", "pr.approve", "pr.rebase",
		"pr.forcePush", "branch.update"} {
		if IsRegisteredVerb(v) {
			t.Errorf("verb %q must NOT be registered", v)
		}
	}
	if got := len(RegisteredVerbs()); got != len(registered) {
		t.Errorf("RegisteredVerbs len = %d, want %d", got, len(registered))
	}
}

func TestIsCLIExecutableVerb(t *testing.T) {
	local := []string{VerbNoop, VerbBudgetRaiseCeiling, VerbRunRetryWithEscalation}
	for _, v := range local {
		if !IsCLIExecutableVerb(v) {
			t.Errorf("verb %q should be CLI-executable (local 3-verb subset)", v)
		}
	}

	daemonOnly := []string{
		VerbQueueAdd, VerbIssueRemoveBlockedBy, VerbAutonomousResume, VerbAutonomousRescan,
		VerbAutonomousComplete, VerbAutonomousClearIssueFailures, VerbProjectSyncStatus,
		VerbIssueClose, VerbIssueApproveArchitecture, VerbDependabotEnableAlerts,
		VerbWorkspaceAddRepo, VerbBlockedFindingClear, VerbPRUpdateBranch,
		"unregistered.verb",
	}
	for _, v := range daemonOnly {
		if IsCLIExecutableVerb(v) {
			t.Errorf("verb %q should NOT be CLI-executable without a daemon", v)
		}
	}
}

func TestValidateOptionRejectsUnknownAndUnregistered(t *testing.T) {
	req := &DecisionRequest{
		ID: "dr_x",
		Options: []Option{
			{ID: "ok", Verb: VerbNoop},
			{ID: "bad-verb", Verb: "not-a-verb"},
		},
	}
	if _, err := ValidateOption(req, "ok"); err != nil {
		t.Errorf("valid option rejected: %v", err)
	}
	if _, err := ValidateOption(req, "missing"); err == nil {
		t.Error("unknown option id should be rejected")
	}
	if _, err := ValidateOption(req, "bad-verb"); err == nil {
		t.Error("option binding an unregistered verb should be rejected")
	}
}

// --- dependabot.enableAlerts (#344) -----------------------------------------

// recordingEnabler captures the (owner, repo) the verb resolved, so "the forge
// was never touched" is an assertion rather than an inference.
type recordingEnabler struct {
	targets []string
	err     error
}

func (e *recordingEnabler) EnableSecurityAlerts(_ context.Context, owner, repo string) error {
	e.targets = append(e.targets, owner+"/"+repo)
	return e.err
}

func enableAlertsCard(repo string) *DecisionRequest {
	return &DecisionRequest{
		ID:             "dr_cov",
		IdempotencyKey: "dependabot-coverage:" + repo,
		Producer:       "dependabot-coverage",
		Context:        Context{Repo: repo},
		Options: []Option{
			{ID: "enable", Verb: VerbDependabotEnableAlerts},
			{ID: "dismiss", Verb: VerbNoop},
		},
	}
}

var enableOpt = Option{ID: "enable", Verb: VerbDependabotEnableAlerts}

// The verb enables alerts on a repo the configured list covers — matched the
// way the workspace sweep matches coverage, so a manifest entry written as
// either `name` or `owner/name` authorises the repo the producer carded.
func TestExecuteEnableAlerts_EnablesAConfiguredRepo(t *testing.T) {
	for _, configured := range [][]string{
		{"acme/web"},
		{"web"},
		{"ACME/WEB"},
		{"acme/api", "acme/web", "acme/jobs"},
	} {
		e := &recordingEnabler{}
		if err := ExecuteEnableAlerts(context.Background(), e, enableAlertsCard("acme/web"), enableOpt, configured); err != nil {
			t.Fatalf("configured %v: %v", configured, err)
		}
		if want := []string{"acme/web"}; !reflect.DeepEqual(e.targets, want) {
			t.Errorf("configured %v: enabled %v, want %v", configured, e.targets, want)
		}
	}
}

// THE security property: the allowed target set is CONFIGURATION. A repo the
// operator never configured cannot be reached, and the forge is not called at
// all — a refusal that still fired the request would be no refusal.
func TestExecuteEnableAlerts_RefusesATargetOutsideTheConfiguredList(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cardRepo   string
		configured []string
	}{
		{"different owner", "attacker/web", []string{"acme/web"}},
		{"different repo", "acme/secrets", []string{"acme/web"}},
		{"nothing configured", "acme/web", nil},
		{"empty configured entries", "acme/web", []string{"", "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &recordingEnabler{}
			err := ExecuteEnableAlerts(context.Background(), e, enableAlertsCard(tc.cardRepo), enableOpt, tc.configured)
			if !errors.Is(err, ErrVerbTargetNotConfigured) {
				t.Fatalf("want ErrVerbTargetNotConfigured, got %v", err)
			}
			if len(e.targets) != 0 {
				t.Fatalf("the forge must not be called for a refused target, got %v", e.targets)
			}
		})
	}
}

// The target comes from the PERSISTED request, never from the resolve payload.
// Args are rejected rather than ignored: an ignored argument is an invitation
// to add policy at the surface later, which is how a bounded verb becomes a
// general one.
func TestExecuteEnableAlerts_RefusesCallerSuppliedPolicy(t *testing.T) {
	configured := []string{"acme/web"}
	for _, args := range []map[string]any{
		{"repo": "attacker/evil"},
		{"owner": "attacker"},
		{"enable": true},
		{"then": "autonomous.resume"},
	} {
		e := &recordingEnabler{}
		opt := Option{ID: "enable", Verb: VerbDependabotEnableAlerts, Args: args}
		err := ExecuteEnableAlerts(context.Background(), e, enableAlertsCard("acme/web"), opt, configured)
		if !errors.Is(err, ErrVerbArgsNotAccepted) {
			t.Fatalf("args %v: want ErrVerbArgsNotAccepted, got %v", args, err)
		}
		if len(e.targets) != 0 {
			t.Fatalf("args %v: the forge must not be called, got %v", args, e.targets)
		}
	}
}

// Defensive arms that must fail loudly rather than resolve the card: the store
// CAS-resolves only after the verb returns nil, so a silent success here
// consumes the card and leaves scanning off.
func TestExecuteEnableAlerts_FailsLoudlyRatherThanSilentlySucceeding(t *testing.T) {
	configured := []string{"acme/web"}

	if err := ExecuteEnableAlerts(context.Background(), &recordingEnabler{}, nil, enableOpt, configured); err == nil {
		t.Error("no request: want an error")
	}
	if err := ExecuteEnableAlerts(context.Background(), &recordingEnabler{},
		enableAlertsCard("acme/web"), Option{ID: "x", Verb: VerbNoop}, configured); err == nil {
		t.Error("mis-dispatched verb: want an error")
	}
	if err := ExecuteEnableAlerts(context.Background(), &recordingEnabler{},
		enableAlertsCard(""), enableOpt, configured); err == nil {
		t.Error("request naming no repo: want an error")
	}
	if err := ExecuteEnableAlerts(context.Background(), &recordingEnabler{},
		enableAlertsCard("not-owner-slash-name"), enableOpt, []string{"not-owner-slash-name"}); err == nil {
		t.Error("target that is not owner/name: want an error")
	}
	// A surface with no forge capability must say so, not report success.
	if err := ExecuteEnableAlerts(context.Background(), nil,
		enableAlertsCard("acme/web"), enableOpt, configured); err == nil {
		t.Error("no enabler: want an error")
	}
	// A forge failure propagates, so the card stays open for a retry.
	boom := errors.New("forge said no")
	if err := ExecuteEnableAlerts(context.Background(), &recordingEnabler{err: boom},
		enableAlertsCard("acme/web"), enableOpt, configured); !errors.Is(err, boom) {
		t.Errorf("forge failure must propagate, got %v", err)
	}
}

func TestRepoInConfiguredSet(t *testing.T) {
	configured := []string{"acme/web", "jobs", " acme/api "}
	for _, repo := range []string{"acme/web", "web", "ACME/Web", "jobs", "acme/jobs", "acme/api", "api"} {
		if !RepoInConfiguredSet(configured, repo) {
			t.Errorf("RepoInConfiguredSet(%q) = false, want true", repo)
		}
	}
	for _, repo := range []string{"acme/other", "other", "", "   ", "acme/web/extra"} {
		if RepoInConfiguredSet(configured, repo) {
			t.Errorf("RepoInConfiguredSet(%q) = true, want false", repo)
		}
	}
}

// --- workspace.addRepo (#706) -----------------------------------------------

type recordingAdder struct {
	targets []string
	err     error
}

func (a *recordingAdder) AddWorkspaceRepo(_ context.Context, repo string) error {
	a.targets = append(a.targets, repo)
	return a.err
}

func coverageGapCard(repo string) *DecisionRequest {
	return &DecisionRequest{
		ID:             "dr_gap",
		IdempotencyKey: "coverage-gap:" + repo,
		Producer:       "coverage-gap",
		Context:        Context{Repo: repo},
		Options: []Option{
			{ID: "add", Verb: VerbWorkspaceAddRepo},
			{ID: "dismiss", Verb: VerbNoop},
		},
	}
}

var addRepoOpt = Option{ID: "add", Verb: VerbWorkspaceAddRepo}

func TestWorkspaceAddRepo_IsRegistered(t *testing.T) {
	if !IsRegisteredVerb(VerbWorkspaceAddRepo) {
		t.Fatal("workspace.addRepo must be in the closed allowlist or no card can bind it")
	}
	// Daemon-executed: resolving the project board goes through the
	// authoritative resolver, which a standalone CLI process does not hold.
	if IsCLIExecutableVerb(VerbWorkspaceAddRepo) {
		t.Error("workspace.addRepo must NOT be CLI-executable — the CLI cannot resolve a project board")
	}
}

func TestExecuteAddRepo_AddsAnUncoveredRepo(t *testing.T) {
	// Configured list covers something else entirely, so the card's target is
	// genuinely uncovered.
	for _, configured := range [][]string{
		{"acme/web"},
		{"web"},
		nil,
	} {
		a := &recordingAdder{}
		if err := ExecuteAddRepo(context.Background(), a, coverageGapCard("acme/docs"), addRepoOpt, configured); err != nil {
			t.Fatalf("configured=%v: ExecuteAddRepo() error: %v", configured, err)
		}
		if len(a.targets) != 1 || a.targets[0] != "acme/docs" {
			t.Errorf("configured=%v: targets = %v, want [acme/docs]", configured, a.targets)
		}
	}
}

// The target comes from the persisted request and nowhere else: the option
// cannot name a repository, so a resolving surface cannot redirect the write.
func TestExecuteAddRepo_TargetComesFromTheRequestOnly(t *testing.T) {
	a := &recordingAdder{}
	opt := Option{ID: "add", Verb: VerbWorkspaceAddRepo, Args: map[string]any{"repo": "acme/evil"}}

	err := ExecuteAddRepo(context.Background(), a, coverageGapCard("acme/docs"), opt, nil)

	if !errors.Is(err, ErrVerbArgsNotAccepted) {
		t.Fatalf("error = %v, want ErrVerbArgsNotAccepted — args must be rejected, not ignored", err)
	}
	if len(a.targets) != 0 {
		t.Errorf("adder was called with %v; a rejected resolution must not mutate", a.targets)
	}
}

// Inverse of every other verb's check: this one exists to act on a repository
// that is NOT configured, so an already-configured target is nothing-to-do.
func TestExecuteAddRepo_RefusesAnAlreadyConfiguredRepo(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cardRepo   string
		configured []string
	}{
		{"exact spec", "acme/docs", []string{"acme/docs"}},
		{"bare name in config", "acme/docs", []string{"docs"}},
		{"bare name on card", "docs", []string{"acme/docs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &recordingAdder{}
			err := ExecuteAddRepo(context.Background(), a, coverageGapCard(tc.cardRepo), addRepoOpt, tc.configured)
			if !errors.Is(err, ErrVerbTargetAlreadyConfigured) {
				t.Fatalf("error = %v, want ErrVerbTargetAlreadyConfigured", err)
			}
			if len(a.targets) != 0 {
				t.Errorf("adder called with %v; the repo was already covered", a.targets)
			}
		})
	}
}

func TestExecuteAddRepo_RefusesAnEmptyTarget(t *testing.T) {
	if err := ExecuteAddRepo(context.Background(), &recordingAdder{}, coverageGapCard(""), addRepoOpt, nil); err == nil {
		t.Fatal("a request naming no repository must fail")
	}
}

func TestExecuteAddRepo_RefusesTheWrongVerb(t *testing.T) {
	opt := Option{ID: "add", Verb: VerbNoop}
	if err := ExecuteAddRepo(context.Background(), &recordingAdder{}, coverageGapCard("acme/docs"), opt, nil); err == nil {
		t.Fatal("the executor must refuse an option bound to a different verb")
	}
}

// A surface without the capability must fail loudly: the store CAS-resolves
// only after the verb returns nil, so a silent success would consume the card
// and leave the repository exactly as unwatched as before.
func TestExecuteAddRepo_RefusesWhenTheSurfaceCannotPerformIt(t *testing.T) {
	err := ExecuteAddRepo(context.Background(), nil, coverageGapCard("acme/docs"), addRepoOpt, nil)
	if err == nil {
		t.Fatal("a surface with no adder must fail rather than silently succeed")
	}
	if !strings.Contains(err.Error(), "not available on this surface") {
		t.Errorf("error = %q, want it to say the surface cannot perform the verb", err)
	}
}

// A failing writer must propagate, for the same CAS reason.
func TestExecuteAddRepo_PropagatesWriterFailure(t *testing.T) {
	a := &recordingAdder{err: errors.New("no project board exists for acme/docs")}
	err := ExecuteAddRepo(context.Background(), a, coverageGapCard("acme/docs"), addRepoOpt, nil)
	if err == nil {
		t.Fatal("a writer failure must propagate so the card stays open")
	}
	if !strings.Contains(err.Error(), "no project board") {
		t.Errorf("error = %q, want the writer's own message preserved", err)
	}
}

// --- pr.updateBranch (#1575) -------------------------------------------------

// recordingUpdater captures every (owner, repo, number) the verb resolved, so
// "the forge was never touched" is an assertion rather than an inference.
type recordingUpdater struct {
	targets []string
	err     error
}

func (u *recordingUpdater) UpdatePRBranch(_ context.Context, owner, repo string, number int) error {
	u.targets = append(u.targets, fmt.Sprintf("%s/%s#%d", owner, repo, number))
	return u.err
}

func behindCard(repo string, pr int) *DecisionRequest {
	return &DecisionRequest{
		ID:             "dr_gate",
		IdempotencyKey: fmt.Sprintf("human-gate:%s#%d", repo, pr),
		Producer:       "human-gate",
		Context:        Context{Repo: repo, PR: pr},
		Options: []Option{
			{ID: "update-branch", Verb: VerbPRUpdateBranch},
			{ID: "dismiss", Verb: VerbNoop},
		},
	}
}

var updateBranchOpt = Option{ID: "update-branch", Verb: VerbPRUpdateBranch}

func TestExecuteUpdatePRBranch_UpdatesAConfiguredRepo(t *testing.T) {
	// Matched the way the workspace sweep matches coverage, so a manifest entry
	// written as either `name` or `owner/name` authorises the repo the producer
	// carded.
	for _, configured := range [][]string{
		{"acme/web"},
		{"web"},
		{"other/thing", "acme/web"},
	} {
		u := &recordingUpdater{}
		if err := ExecuteUpdatePRBranch(context.Background(), u, behindCard("acme/web", 1546), updateBranchOpt, configured); err != nil {
			t.Fatalf("configured=%v: ExecuteUpdatePRBranch() error: %v", configured, err)
		}
		if len(u.targets) != 1 || u.targets[0] != "acme/web#1546" {
			t.Fatalf("configured=%v: forge saw %v, want [acme/web#1546]", configured, u.targets)
		}
	}
}

// BOTH coordinates come from the persisted request and nowhere else. A
// caller-supplied PR number would turn a card about one stale PR into a general
// "merge base into any PR in a configured repo" primitive, so args are refused
// outright rather than ignored — an ignored argument is a silent invitation to
// add policy at the surface later.
func TestExecuteUpdatePRBranch_RefusesCallerSuppliedPolicy(t *testing.T) {
	for _, args := range []map[string]any{
		{"number": 99},
		{"repo": "attacker/evil"},
		{"owner": "attacker"},
		{"force": true},
	} {
		u := &recordingUpdater{}
		opt := Option{ID: "update-branch", Verb: VerbPRUpdateBranch, Args: args}
		err := ExecuteUpdatePRBranch(context.Background(), u, behindCard("acme/web", 1546), opt, []string{"acme/web"})
		if !errors.Is(err, ErrVerbArgsNotAccepted) {
			t.Fatalf("args=%v: error = %v, want ErrVerbArgsNotAccepted", args, err)
		}
		if len(u.targets) != 0 {
			t.Fatalf("args=%v: the forge was reached: %v", args, u.targets)
		}
	}
}

// The configured list is the security boundary: it comes from the thing an
// operator edits deliberately, never from the request being resolved.
func TestExecuteUpdatePRBranch_RefusesATargetOutsideTheConfiguredList(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cardRepo   string
		configured []string
	}{
		{"foreign repo", "attacker/evil", []string{"acme/web"}},
		{"nothing configured", "acme/web", nil},
		{"near-miss name", "acme/web-staging", []string{"acme/web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &recordingUpdater{}
			err := ExecuteUpdatePRBranch(context.Background(), u, behindCard(tc.cardRepo, 7), updateBranchOpt, tc.configured)
			if !errors.Is(err, ErrVerbTargetNotConfigured) {
				t.Fatalf("error = %v, want ErrVerbTargetNotConfigured", err)
			}
			if len(u.targets) != 0 {
				t.Fatalf("the forge was reached: %v", u.targets)
			}
		})
	}
}

// The store CAS-resolves only after the verb returns nil, so every refusal here
// must be loud. A silent success would consume the card and leave the PR
// exactly as stuck as before, with the one affordance that could have fixed it
// now gone.
func TestExecuteUpdatePRBranch_FailsLoudlyRatherThanSilentlySucceeding(t *testing.T) {
	if err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{}, nil, updateBranchOpt, []string{"acme/web"}); err == nil {
		t.Error("a nil request must be refused")
	}
	if err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{},
		behindCard("", 7), updateBranchOpt, []string{"acme/web"}); err == nil {
		t.Error("a request naming no repository must be refused")
	}
	if err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{},
		behindCard("acme/web", 0), updateBranchOpt, []string{"acme/web"}); err == nil {
		t.Error("a request naming no pull request must be refused")
	}
	if err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{},
		behindCard("notownerrepo", 7), updateBranchOpt, []string{"notownerrepo"}); err == nil {
		t.Error("a target that is not owner/name must be refused")
	}
	if err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{},
		behindCard("acme/web", 7), Option{ID: "x", Verb: VerbNoop}, []string{"acme/web"}); err == nil {
		t.Error("a mis-dispatched verb must be refused")
	}
	// A surface without the capability says so rather than succeeding.
	if err := ExecuteUpdatePRBranch(context.Background(), nil,
		behindCard("acme/web", 7), updateBranchOpt, []string{"acme/web"}); err == nil {
		t.Error("an executor with no updater must be refused")
	}
	// A real forge failure propagates — the card must survive to say the PR is
	// still behind.
	boom := errors.New("update-branch: 409")
	err := ExecuteUpdatePRBranch(context.Background(), &recordingUpdater{err: boom},
		behindCard("acme/web", 7), updateBranchOpt, []string{"acme/web"})
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the forge failure", err)
	}
}
