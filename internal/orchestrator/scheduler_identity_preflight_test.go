package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// fakeIdentityChecker is a deterministic IdentityChecker for the preflight gate
// test (#4068). It records the (owner, repo) it was asked about and returns a
// canned verdict so the dispatch gate can be exercised without network I/O.
type fakeIdentityChecker struct {
	allowed  bool
	reason   string
	gotOwner string
	gotRepo  string
	called   bool
}

func (f *fakeIdentityChecker) CheckIdentity(_ context.Context, owner, repo string, _ int) (bool, string) {
	f.called = true
	f.gotOwner = owner
	f.gotRepo = repo
	return f.allowed, f.reason
}

func newPreflightScheduler(checker IdentityChecker) *Scheduler {
	return &Scheduler{
		repoRunning:     make(map[string]int),
		mergeLocks:      make(map[string]*sync.Mutex),
		identityChecker: checker,
	}
}

// TestPreflightIdentity_RejectsNoPushIdentity verifies the dispatch gate rejects
// a target repo whose resolved identity lacks push — the failure is surfaced
// with the specific reason (via SetStageError) rather than allowed through to a
// later silent merge failure.
func TestPreflightIdentity_RejectsNoPushIdentity(t *testing.T) {
	checker := &fakeIdentityChecker{
		allowed: false,
		reason:  `identity "octocat" lacks push access on Acme-Community/acmesvc-tracker`,
	}
	s := newPreflightScheduler(checker)

	item := types.BoardItem{Repo: "Acme-Community/acmesvc-tracker", Number: 143, ID: "I_143"}
	rt := state.NewRuntimeState(item.Repo, item.Number, item.ID)

	ok, reason := s.preflightIdentity(context.Background(), item, rt)
	if ok {
		t.Fatalf("expected preflightIdentity to BLOCK a no-push identity, got allowed=true")
	}
	if !strings.Contains(reason, "lacks push access") {
		t.Errorf("reason = %q, want it to name the push blocker", reason)
	}
	// The checker must have been asked about the parsed owner/repo.
	if checker.gotOwner != "Acme-Community" || checker.gotRepo != "acmesvc-tracker" {
		t.Errorf("checker called with (%q,%q), want (Acme-Community, acmesvc-tracker)", checker.gotOwner, checker.gotRepo)
	}
	// The specific reason is recorded on the runtime so the run surfaces a
	// pipeline-failed outcome naming the blocker (not a phantom success).
	snap := rt.Snapshot()
	stageErr, ok2 := snap.StageErrors["pipeline-start"]
	if !ok2 {
		t.Fatalf("expected a pipeline-start stage error to be recorded; StageErrors=%v", snap.StageErrors)
	}
	if !strings.Contains(stageErr, "identity preflight") || !strings.Contains(stageErr, "lacks push access") {
		t.Errorf("recorded stage error = %q, want it to carry the identity-preflight reason", stageErr)
	}
}

// TestPreflightIdentity_SkippedWhenUnconfigured verifies the gate is disabled
// when no IdentityChecker is wired (CLI mode / single-identity repos): the run
// is allowed and no stage error is recorded.
func TestPreflightIdentity_SkippedWhenUnconfigured(t *testing.T) {
	s := newPreflightScheduler(nil) // no checker → gate disabled

	item := types.BoardItem{Repo: "nightgauge/nightgauge", Number: 1, ID: "I_1"}
	rt := state.NewRuntimeState(item.Repo, item.Number, item.ID)

	ok, reason := s.preflightIdentity(context.Background(), item, rt)
	if !ok {
		t.Fatalf("expected preflightIdentity to ALLOW when no checker is wired, got blocked: %s", reason)
	}
	if _, exists := rt.Snapshot().StageErrors["pipeline-start"]; exists {
		t.Errorf("no stage error should be recorded when the gate is skipped")
	}
}

// TestPreflightIdentity_AllowsWhenIdentityHasPush verifies a configured identity
// with push passes the gate cleanly.
func TestPreflightIdentity_AllowsWhenIdentityHasPush(t *testing.T) {
	checker := &fakeIdentityChecker{allowed: true}
	s := newPreflightScheduler(checker)

	item := types.BoardItem{Repo: "Acme-Community/acmesvc-tracker", Number: 144, ID: "I_144"}
	rt := state.NewRuntimeState(item.Repo, item.Number, item.ID)

	ok, _ := s.preflightIdentity(context.Background(), item, rt)
	if !ok {
		t.Fatalf("expected preflightIdentity to ALLOW a push-capable identity")
	}
	if !checker.called {
		t.Error("checker should have been consulted")
	}
	if _, exists := rt.Snapshot().StageErrors["pipeline-start"]; exists {
		t.Errorf("no stage error should be recorded on a passing assertion")
	}
}

// TestPreflightIdentity_SkipsUnqualifiedRepo verifies an unqualified repo (no
// owner/name) does not block the run on a parsing gap — the gate allows rather
// than failing closed on a malformed item.
func TestPreflightIdentity_SkipsUnqualifiedRepo(t *testing.T) {
	checker := &fakeIdentityChecker{allowed: false, reason: "should not be consulted"}
	s := newPreflightScheduler(checker)

	item := types.BoardItem{Repo: "nightgauge", Number: 2, ID: "I_2"} // no owner/
	rt := state.NewRuntimeState(item.Repo, item.Number, item.ID)

	ok, _ := s.preflightIdentity(context.Background(), item, rt)
	if !ok {
		t.Fatalf("expected an unqualified repo to be skipped (allowed), got blocked")
	}
	if checker.called {
		t.Error("checker must not be consulted for an unqualified repo")
	}
}

// TestConfigIdentityChecker_SkipsWhenNoGitHubUser verifies the production
// checker treats "no github_user configured for the owner" as allowed (skip) so
// single-identity workspaces are unaffected — and that it does so WITHOUT
// building a client (no network).
func TestConfigIdentityChecker_SkipsWhenNoGitHubUser(t *testing.T) {
	cfg := &config.Config{Owner: "nightgauge"} // no github_user, no github_auth.users
	checker := NewConfigIdentityChecker(cfg)
	if checker == nil {
		t.Fatal("expected non-nil checker for non-nil config")
	}
	clientBuilt := false
	checker.newClient = func(_ *config.Config, _ string) (*gh.Client, error) {
		clientBuilt = true
		return nil, nil
	}

	ok, reason := checker.CheckIdentity(context.Background(), "nightgauge", "nightgauge", 1)
	if !ok {
		t.Fatalf("expected skip (allowed) when no github_user is configured, got blocked: %s", reason)
	}
	if clientBuilt {
		t.Error("checker should NOT build a client when there is no configured identity to assert")
	}
}

// TestNewConfigIdentityChecker_NilConfig verifies a nil config yields a nil
// checker so the scheduler keeps the gate disabled.
func TestNewConfigIdentityChecker_NilConfig(t *testing.T) {
	if c := NewConfigIdentityChecker(nil); c != nil {
		t.Errorf("NewConfigIdentityChecker(nil) = %v, want nil", c)
	}
}
