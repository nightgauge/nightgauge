package attention

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRegistryIsClosedAllowlist(t *testing.T) {
	registered := []string{
		VerbQueueAdd, VerbIssueRemoveBlockedBy, VerbAutonomousResume, VerbAutonomousRescan,
		VerbAutonomousComplete, VerbAutonomousClearIssueFailures, VerbProjectSyncStatus,
		VerbIssueClose, VerbBudgetRaiseCeiling, VerbRunRetryWithEscalation,
		VerbIssueApproveArchitecture, VerbDependabotEnableAlerts, VerbNoop,
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
	// Anything not on the allowlist is rejected — the security boundary. The
	// near-miss spellings of the approval verb must NOT resolve: the executor
	// resolves the label name from config, so a surface cannot reach a
	// generic label-mutating verb even by guessing a plausible name. The same
	// holds for the settings verb: nothing generic ("repo.updateSettings") and
	// nothing adjacent ("dependabot.disableAlerts") is reachable by guessing.
	for _, v := range []string{"rm", "shell.exec", "queue.remove", "", "budget.raise",
		"issue.addLabel", "issue.approveArchitecture ", "issue.approve",
		"dependabot.enableAlerts ", "dependabot.enable", "dependabot.disableAlerts",
		"repo.updateSettings", "security.enable"} {
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
