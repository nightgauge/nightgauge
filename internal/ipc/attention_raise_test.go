package ipc

// Tests for the run-scoped raise verb (#305).
//
// The load-bearing test is TestAttentionRaiseProducesTheGoPathCard: for the
// same condition, the card the EXTENSION path writes must be the card the GO
// path writes, field for field. Before this change the extension path wrote no
// card at all, so that test could not even be expressed.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// recordRunSpend seeds the DAEMON's own record of what a run has spent — the
// only source attention.raise will accept for the budget-ceiling card's
// numbers (#305 review). Mirrors what pipeline.notifyStageTransition
// accumulates on the extension path.
func recordRunSpend(t *testing.T, s *Server, repo string, issue int, costUSD float64) {
	t.Helper()
	rt := state.NewRuntimeState(repo, issue, "")
	rt.TotalCostUSD = costUSD
	s.runtimesMu.Lock()
	s.activeRuntimes[fmt.Sprintf("%d", issue)] = rt
	s.runtimesMu.Unlock()
}

func raiseParams(t *testing.T, p AttentionRaiseParams) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

func mustRaise(t *testing.T, s *Server, p AttentionRaiseParams) AttentionRaiseResult {
	t.Helper()
	res, err := s.handleAttentionRaise(context.Background(), raiseParams(t, p))
	if err != nil {
		t.Fatalf("handleAttentionRaise(%s): %v", p.Producer, err)
	}
	out, ok := res.(AttentionRaiseResult)
	if !ok {
		t.Fatalf("result type = %T, want AttentionRaiseResult", res)
	}
	return out
}

// identity is the subset of a DecisionRequest that must be identical across
// the two paths. It deliberately excludes `id` (minted per raise) and the two
// timestamps (created_at / expires_at move with the clock).
type identity struct {
	SchemaVersion int
	Key           string
	Producer      string
	Kind          attention.Kind
	Severity      attention.Severity
	Title         string
	Body          string
	Options       []attention.Option
	DefaultAction string
	Standing      bool
	Fingerprint   string
	Context       attention.Context
	Steer         *attention.Steer
}

// identityJSON renders the identity subset as canonical JSON. The comparison
// goes through JSON deliberately: the persisted record is read back off disk,
// so its option Args have already been through a JSON round-trip (an int
// becomes a float64) while an in-memory expectation has not. Comparing the
// serialized forms compares what is actually on disk, which is what a surface
// reads.
func identityJSON(t *testing.T, r attention.DecisionRequest) string {
	t.Helper()
	raw, err := json.Marshal(identityOf(r))
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatalf("normalize identity: %v", err)
	}
	out, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		t.Fatalf("re-marshal identity: %v", err)
	}
	return string(out)
}

func identityOf(r attention.DecisionRequest) identity {
	return identity{
		SchemaVersion: r.SchemaVersion,
		Key:           r.IdempotencyKey,
		Producer:      r.Producer,
		Kind:          r.Kind,
		Severity:      r.Severity,
		Title:         r.Title,
		Body:          r.Body,
		Options:       r.Options,
		DefaultAction: r.DefaultAction,
		Standing:      r.Standing,
		Fingerprint:   r.Fingerprint,
		Context:       r.Context,
		Steer:         r.Steer,
	}
}

func onlyOpenRequest(t *testing.T, s *Server) attention.DecisionRequest {
	t.Helper()
	reqs, err := s.attentionStore().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("open requests = %d, want exactly 1", len(reqs))
	}
	return reqs[0]
}

// TestAttentionRaiseProducesTheGoPathCard is the issue's acceptance criterion:
// a run-scoped condition observed on the EXTENSION path yields the same card as
// the same condition observed by the Go scheduler — same producer, same
// idempotency key, same kind/severity, same body, same options bound to the
// same verbs with the same args.
//
// The Go-path expectation is not a literal transcribed into this file: it is
// orchestrator.BuildBudgetCeilingHit, the exact function
// (*Scheduler).raiseBudgetCeilingHit calls. TestRunScopedProducersDelegateToSharedBuilders
// in internal/orchestrator pins the other half — that the scheduler call site
// really is that builder and nothing else.
func TestAttentionRaiseProducesTheGoPathCard(t *testing.T) {
	const (
		repo  = "octocat/acme"
		issue = 4242
		runID = "019f0000-0000-7000-8000-000000000001"
		cost  = 12.50
	)
	s := newAttentionTestServer(t)
	recordRunSpend(t, s, repo, issue, cost)
	// The enforced ceiling is read in-process, exactly as scheduler.go reads it.
	ceiling := orchestrator.PipelineBudgetCeilingUSD(s.workspaceRoot)

	got := mustRaise(t, s, AttentionRaiseParams{
		Producer: ProducerBudgetCeiling,
		Repo:     repo,
		Issue:    issue,
		RunID:    runID,
	})
	if got.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("outcome = %q, want %q", got.Outcome, attention.OutcomeCreated)
	}

	persisted := onlyOpenRequest(t, s)
	want := orchestrator.BuildBudgetCeilingHit(
		repo, issue, runID, cost, orchestrator.ProposedCeilingUSD(ceiling, cost))
	want.SchemaVersion = attention.SchemaVersion // applied by Raise on persist

	if got, wantJSON := identityJSON(t, persisted), identityJSON(t, want); got != wantJSON {
		t.Errorf("extension-path card != Go-path card\n got: %s\nwant: %s", got, wantJSON)
	}

	// Spot-check the fields the issue names explicitly, so a failure reads as
	// something other than one enormous struct diff.
	if persisted.Producer != ProducerBudgetCeiling {
		t.Errorf("producer = %q, want %q", persisted.Producer, ProducerBudgetCeiling)
	}
	if persisted.Context.RunID != runID {
		t.Errorf("context.run_id = %q, want %q — the card is not run-scoped", persisted.Context.RunID, runID)
	}
	if persisted.Context.TraceRef == nil || persisted.Context.TraceRef.RunID != runID {
		t.Errorf("context.trace_ref = %+v, want a back-reference to the run", persisted.Context.TraceRef)
	}
	// The proposed ceiling is derived daemon-side from ONE rule, so the
	// extension cannot propose a different number than the scheduler would.
	raise := persisted.FindOption("raise")
	if raise == nil {
		t.Fatalf("no `raise` option on the card: %+v", persisted.Options)
	}
	if raise.Verb != attention.VerbBudgetRaiseCeiling {
		t.Errorf("raise option verb = %q, want %q", raise.Verb, attention.VerbBudgetRaiseCeiling)
	}
	if gotCeil, ok := raise.Args["ceilingUsd"].(float64); !ok || gotCeil != orchestrator.ProposedCeilingUSD(ceiling, cost) {
		t.Errorf("raise option ceilingUsd = %v, want %v", raise.Args["ceilingUsd"], orchestrator.ProposedCeilingUSD(ceiling, cost))
	}
}

// TestAttentionRaiseDedupesOnIdempotencyKey — the same condition observed twice
// updates one card in place. Without dedup, a between-stage ceiling check that
// fires on a re-dispatch would stack cards for one overrun.
func TestAttentionRaiseDedupesOnIdempotencyKey(t *testing.T) {
	s := newAttentionTestServer(t)
	recordRunSpend(t, s, "octocat/acme", 7, 20)
	p := AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: "octocat/acme", Issue: 7,
		RunID: "run-1",
	}

	first := mustRaise(t, s, p)
	if first.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("first outcome = %q, want created", first.Outcome)
	}

	recordRunSpend(t, s, "octocat/acme", 7, 25) // the run kept spending; same condition, same key
	second := mustRaise(t, s, p)
	if second.Outcome != string(attention.OutcomeUpdated) {
		t.Errorf("second outcome = %q, want updated", second.Outcome)
	}
	if second.ID != first.ID {
		t.Errorf("second id = %q, want the first card's id %q", second.ID, first.ID)
	}

	files, err := filepath.Glob(filepath.Join(s.attentionStore().Dir(), "dr_*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("materialized records = %d, want 1 — duplicate raise created a second card", len(files))
	}
}

// TestAbandonedDispatchReRaisesAfterAHumanResolution — a force-clear that
// happens AFTER the operator dealt with the previous one is a NEW FACT and must
// produce a card.
//
// This is the regression test for the defect the first cut of #305 shipped.
// BuildAbandonedDispatch declared `Standing: true` with the constant
// fingerprint "abandoned:force-clear", which opted it into ADR-015 §M: a
// standing raise whose fingerprint equals the latest human-RESOLVED record for
// its key returns `suppressed` and writes nothing. A constant fingerprint can
// never move, resolved records are never pruned, and nothing calls
// AutoResolveUnobserved / AutoResolveKey for this producer — so the FIRST
// resolution silenced that (repo, issue) forever.
//
// The silenced path is the card's own designed flow, not a corner: the primary
// option is Retry, which resolves the card and re-dispatches the issue into the
// same preserved worktree; when that dispatch wedges again — the ordinary
// outcome for a wedged worktree, and the entire reason #307's force-clear
// funnel exists — the operator was never told. Verbatim, against the unfixed
// builder:
//
//	first outcome=created id=dr_019fdf95-4952-78ae-b4a9-d61f2133a5b2
//	second (new run, new stage) outcome=suppressed id=dr_019fdf95-4952-…
//	open cards after second wedge: 0
//
// The option used to dismiss is immaterial: §M keys on the RESOLVED state, not
// on which option produced it, so every dismissal silenced the issue
// identically. (The card's options are both noops now — see
// TestAbandonedDispatchCardIsInformationalNotARetry.)
func TestAbandonedDispatchReRaisesAfterAHumanResolution(t *testing.T) {
	s := newAttentionTestServer(t)
	p := AttentionRaiseParams{
		Producer: ProducerAbandonedDispatch, Repo: "octocat/acme", Issue: 9,
		RunID: "run-1", Stage: "feature-dev",
	}

	first := mustRaise(t, s, p)
	if first.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("first outcome = %q, want created", first.Outcome)
	}
	if _, err := s.attentionStore().Resolve(context.Background(), first.ID, "acknowledged", "octocat", "", "", s); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// A genuinely different wedge: another dispatch, another stage.
	p.RunID = "run-2"
	p.Stage = "pr-create"
	second := mustRaise(t, s, p)
	if second.Outcome != string(attention.OutcomeCreated) {
		t.Errorf("force-clear after a resolution = %q, want created — the operator's retry "+
			"died and nothing told them", second.Outcome)
	}
	open, err := s.attentionStore().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open cards after the second wedge = %d, want 1", len(open))
	}
	if open[0].Context.Stage != "pr-create" {
		t.Errorf("open card stage = %q, want the SECOND wedge's stage", open[0].Context.Stage)
	}
}

// TestAttentionRaiseCollapsesRepeatForceClearsOntoOneCard — the property the
// standing declaration was reaching for, obtained the way an EVENT gets it:
// Store.Raise updates the open record for the key in place. A slot that wedges,
// is re-queued, and wedges again is one thing to deal with, not a card per
// attempt.
func TestAttentionRaiseCollapsesRepeatForceClearsOntoOneCard(t *testing.T) {
	s := newAttentionTestServer(t)
	p := AttentionRaiseParams{
		Producer: ProducerAbandonedDispatch, Repo: "octocat/acme", Issue: 9,
		RunID: "run-1", Stage: "feature-dev",
	}

	first := mustRaise(t, s, p)
	if first.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("first outcome = %q, want created", first.Outcome)
	}

	p.RunID = "run-2"
	p.Stage = "pr-create"
	second := mustRaise(t, s, p)
	if second.Outcome != string(attention.OutcomeUpdated) {
		t.Errorf("repeat force-clear while the card is open = %q, want updated", second.Outcome)
	}
	if second.ID != first.ID {
		t.Errorf("second id = %q, want the open card's id %q", second.ID, first.ID)
	}
	files, err := filepath.Glob(filepath.Join(s.attentionStore().Dir(), "dr_*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("materialized records = %d, want 1 — a re-wedge spawned a second card", len(files))
	}
	// The refresh carries the LATEST wedge, not the first one's stale stage.
	persisted := onlyOpenRequest(t, s)
	if persisted.Context.Stage != "pr-create" {
		t.Errorf("stage = %q, want the most recent wedge's stage", persisted.Context.Stage)
	}
}

// TestNoRaiseableProducerIsStandingWithoutRetraction is the structural fence
// around the defect above.
//
// STANDING is not a severity dial — it is a contract with two obligations
// (docs/ATTENTION_PRODUCERS.md, "Run-scoped producers"): a fingerprint that
// MOVES when the underlying condition moves, and a trigger site that calls
// autoResolveAttention / AutoResolveKey with what it just observed so the card
// retracts when the condition clears. A producer raised over IPC has neither by
// construction: `attention.raise` is a one-shot report from a surface that
// observed a transition, with no scan to reconcile against. Declaring Standing
// there inherits §M's "a human resolved this exact condition, do not hand it
// back" rule with no way to ever lapse it.
//
// So the rule for this allowlist is flat: no standing producers. If a genuinely
// standing condition ever needs the raise verb, this test is where the
// retraction story has to be written down first.
func TestNoRaiseableProducerIsStandingWithoutRetraction(t *testing.T) {
	s := newAttentionTestServer(t)
	recordRunSpend(t, s, "octocat/acme", 1, 20)
	samples := map[string]AttentionRaiseParams{
		ProducerBudgetCeiling: {Producer: ProducerBudgetCeiling, Repo: "octocat/acme", Issue: 1,
			RunID: "run-1"},
		ProducerBranchProtection: {Producer: ProducerBranchProtection, Repo: "octocat/acme", Issue: 1,
			RunID: "run-1", PR: 2, PRState: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"},
		ProducerAbandonedDispatch: {Producer: ProducerAbandonedDispatch, Repo: "octocat/acme", Issue: 1,
			RunID: "run-1", Stage: "feature-dev"},
	}
	for _, producer := range RaiseableProducers() {
		p, ok := samples[producer]
		if !ok {
			t.Fatalf("producer %q is raiseable but has no sample here — add one and state its "+
				"standing/retraction story", producer)
		}
		req, applicable, err := s.buildRaise(p)
		if err != nil || !applicable {
			t.Fatalf("buildRaise(%s): applicable=%v err=%v — the sample must produce a card", producer, applicable, err)
		}
		if req.Standing {
			t.Errorf("%s declares Standing over the raise verb, which has no scan to auto-resolve "+
				"against: the first human resolution suppresses it forever (ADR-015 §M)", producer)
		}
	}
}

// TestAttentionRaiseRejectsUnknownProducer — the closed allowlist is the
// security boundary. A surface may name a CONDITION; it may never describe a
// card, and it may never name a producer the daemon has no builder for.
func TestAttentionRaiseRejectsUnknownProducer(t *testing.T) {
	s := newAttentionTestServer(t)
	for _, producer := range []string{"", "issue-close-everything", "human-gate", "terminal-failure"} {
		raw := raiseParams(t, AttentionRaiseParams{Producer: producer, Repo: "octocat/acme", Issue: 1})
		if _, err := s.handleAttentionRaise(context.Background(), raw); err == nil {
			t.Errorf("producer %q was accepted, want rejection", producer)
		}
	}
	if reqs, _ := s.attentionStore().List(attention.ListFilter{IncludeTerminal: true}); len(reqs) != 0 {
		t.Errorf("a rejected raise wrote %d record(s), want 0", len(reqs))
	}
}

// TestAttentionRaiseParamsCarryNoExecutableSurface — a structural guard on the
// wire shape. Card options are EXECUTED by the daemon on resolve, so the raise
// params must not carry an options array, a verb, or an args map: a caller that
// could would mint a legitimate-looking card offering `issue.close` on an
// arbitrary issue and wait for a click. This fails the moment someone
// "conveniently" widens the params.
func TestAttentionRaiseParamsCarryNoExecutableSurface(t *testing.T) {
	forbidden := []string{"option", "verb", "arg", "command", "exec", "request", "decisionrequest"}
	typ := reflect.TypeOf(AttentionRaiseParams{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("AttentionRaiseParams.%s: raise params must not carry an executable surface (%q)", f.Name, bad)
			}
		}
		// Nothing in the params may be a free-form map: that is how an args
		// map gets in without being called "args".
		if f.Type.Kind() == reflect.Map {
			t.Errorf("AttentionRaiseParams.%s is a map — the params are typed scalars by design", f.Name)
		}
	}
}

// TestAttentionRaiseBranchProtectionClassifiesDaemonSide — the extension sends
// the raw `gh pr view` projection and the DAEMON classifies with
// stages.Decide, so both paths put the same reason string on the card. If the
// extension sent prose instead, the same block would render two different cards
// depending on which path observed it, and every test would still pass.
func TestAttentionRaiseBranchProtectionClassifiesDaemonSide(t *testing.T) {
	cases := []struct {
		name       string
		params     AttentionRaiseParams
		wantRaised bool
		wantReason string
	}{
		{
			name: "review required is a branch-protection block",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
				ReviewDecision: "REVIEW_REQUIRED",
			},
			wantRaised: true,
			wantReason: "review-not-approved: REVIEW_REQUIRED",
		},
		{
			name: "a failing required check is a branch-protection block",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
				Checks: []AttentionRaiseCheck{
					{Name: "lint", Conclusion: "SUCCESS"},
					{Name: "build-and-test", Conclusion: "FAILURE"},
				},
			},
			wantRaised: true,
			wantReason: "failed-ci-checks: build-and-test",
		},
		{
			name: "a merge conflict is a branch-protection block",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY",
			},
			wantRaised: true,
			wantReason: "not-mergeable: CONFLICTING",
		},
		{
			name: "a mergeable, clean, approved PR is not a block at all",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
				ReviewDecision: "APPROVED",
			},
			wantRaised: false,
		},
		{
			name: "a CLOSED PR is a punt the Go path also stays silent on",
			params: AttentionRaiseParams{
				PRState: "CLOSED", Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN",
			},
			wantRaised: false,
		},
		// --- in-flight CI: the case bare Decide() gets WRONG -----------------
		//
		// A queued required check makes an otherwise-clean PR BLOCKED, which
		// Decide() reasons as `dirty-merge-state: BLOCKED` and
		// IsBranchProtectionPunt matches by prefix. The Go runner never reaches
		// that punt — DeterministicRunner.Run tests MergeBlockedByPendingCI
		// first, waits the bounded budget, and on timeout punts
		// `ci-wait-timeout`, which IsBranchProtectionPunt does not match. The
		// raise handler applies the SAME predicate, so this is not_applicable
		// on both paths.
		//
		// This is not a rare shape. prmerge.go: pr-merge starts immediately
		// after pr-create, so on repos whose CI takes minutes the first
		// snapshot is ALWAYS BLOCKED/UNSTABLE with pending checks (#297).
		// Without the exclusion, every such run got a blocking_run card with a
		// 48h TTL and no auto-resolve, saying "Fix the failing check / approval
		// on GitHub" about CI that was about to go green on its own.
		{
			name: "a queued required check (BLOCKED, null conclusion) is in-flight CI, not branch protection",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
				ReviewDecision: "",
				// "" is what GitHub's null conclusion projects to — the wire
				// value that means "has not concluded".
				Checks: []AttentionRaiseCheck{{Name: "build-and-test", Conclusion: ""}},
			},
			wantRaised: false,
		},
		{
			name: "a pending optional check (UNSTABLE) is in-flight CI, not branch protection",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "UNSTABLE",
				Checks: []AttentionRaiseCheck{
					{Name: "lint", Conclusion: "SUCCESS"},
					{Name: "e2e", Conclusion: "PENDING"},
				},
			},
			wantRaised: false,
		},
		{
			name: "a FAILED check alongside a pending one is a hard blocker, card it",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
				Checks: []AttentionRaiseCheck{
					{Name: "build-and-test", Conclusion: "FAILURE"},
					{Name: "e2e", Conclusion: ""},
				},
			},
			wantRaised: true,
			// Decide reports the merge state before it walks the checks, so the
			// reason names the state — the card is still correct that a human
			// is needed, which is what the gate decides.
			wantReason: "dirty-merge-state: BLOCKED",
		},
		{
			name: "review required while CI is still pending will not clear by waiting",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
				ReviewDecision: "REVIEW_REQUIRED",
				Checks:         []AttentionRaiseCheck{{Name: "build-and-test", Conclusion: ""}},
			},
			wantRaised: true,
			wantReason: "dirty-merge-state: BLOCKED",
		},
		{
			name: "BLOCKED with no pending check at all is a real branch-protection block",
			params: AttentionRaiseParams{
				PRState: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED",
				Checks: []AttentionRaiseCheck{{Name: "lint", Conclusion: "SUCCESS"}},
			},
			wantRaised: true,
			wantReason: "dirty-merge-state: BLOCKED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttentionTestServer(t)
			p := tc.params
			p.Producer = ProducerBranchProtection
			p.Repo = "octocat/acme"
			p.Issue = 11
			p.PR = 55
			p.RunID = "run-1"

			got := mustRaise(t, s, p)
			if !tc.wantRaised {
				if got.Outcome != "not_applicable" {
					t.Fatalf("outcome = %q, want not_applicable", got.Outcome)
				}
				if got.ID != "" {
					t.Errorf("not_applicable carried id %q, want empty", got.ID)
				}
				if reqs, _ := s.attentionStore().List(attention.ListFilter{IncludeTerminal: true}); len(reqs) != 0 {
					t.Errorf("not_applicable wrote %d record(s), want 0", len(reqs))
				}
				return
			}
			if got.Outcome != string(attention.OutcomeCreated) {
				t.Fatalf("outcome = %q, want created", got.Outcome)
			}
			persisted := onlyOpenRequest(t, s)
			want := orchestrator.BuildBranchProtectionBlock("octocat/acme", 11, 55, "run-1", tc.wantReason)
			want.SchemaVersion = attention.SchemaVersion
			if got, wantJSON := identityJSON(t, persisted), identityJSON(t, want); got != wantJSON {
				t.Errorf("card != the Go path's card for reason %q\n got: %s\nwant: %s",
					tc.wantReason, got, wantJSON)
			}
			if persisted.Context.Blocker != tc.wantReason {
				t.Errorf("blocker = %q, want the stages.Decide reason %q", persisted.Context.Blocker, tc.wantReason)
			}
			if persisted.Context.PR != 55 {
				t.Errorf("context.pr = %d, want 55 — the sweep's human-gate producer dedupes on it", persisted.Context.PR)
			}
		})
	}
}

// TestAttentionRaiseRequiresProducerFields — a producer missing the inputs its
// card is built from is an ERROR with its own message, never a silent no-op and
// never a card with a zero in it.
func TestAttentionRaiseRequiresProducerFields(t *testing.T) {
	s := newAttentionTestServer(t)
	cases := []struct {
		name string
		p    AttentionRaiseParams
	}{
		{"branch-protection without pr", AttentionRaiseParams{Producer: ProducerBranchProtection, Repo: "o/r", Issue: 1}},
		// Missing merge state must NOT default to UNKNOWN: Decide would read
		// that as not-mergeable and raise a card asserting the PR cannot merge
		// when the truth is that nobody looked.
		{"branch-protection without merge state", AttentionRaiseParams{
			Producer: ProducerBranchProtection, Repo: "o/r", Issue: 1, PR: 2}},
		{"branch-protection without mergeStateStatus", AttentionRaiseParams{
			Producer: ProducerBranchProtection, Repo: "o/r", Issue: 1, PR: 2,
			PRState: "OPEN", Mergeable: "MERGEABLE"}},
		{"no repo", AttentionRaiseParams{Producer: ProducerAbandonedDispatch, Issue: 1}},
		{"no issue", AttentionRaiseParams{Producer: ProducerAbandonedDispatch, Repo: "o/r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.handleAttentionRaise(context.Background(), raiseParams(t, tc.p)); err == nil {
				t.Fatal("expected an error naming the missing field")
			}
		})
	}
}

// --- the round-2 security finding, pinned -----------------------------------
//
// The reported attack, verbatim from the review's executed overlay probe:
//
//	STEP 1 handleAttentionRaise({producer:"budget-ceiling", repo:"victim/repo",
//	       issue:1, costUsd:0.01, ceilingUsd:1000000})
//	    -> outcome=created, option id=raise verb=budget.raiseCeiling
//	       args=map[ceilingUsd:1.5e+06 issueNumber:1 owner:victim repo:repo]
//	STEP 2 store.Resolve(ctx, id, "raise", "attacker", ..., s)
//	    -> wrote .nightgauge/pipeline/budget-override.json
//	       {"schema_version":1,"ceiling_usd":1500000,...}
//
// `PipelineBudgetCeilingUSD` takes max(config, override), so the workspace
// spend control was gone — no breach, no operator click, from any local process
// with socket access (#263).
//
// Three independent things now break that chain, one test each below: the repo
// must be configured (step 1 never reaches a builder for `victim/repo`), the
// ceiling is not a parameter (a caller cannot choose the number), and an
// uncorroborated spend removes the option that does the write.

// TestAttentionRaiseRejectsUnconfiguredRepo — the first step of the chain.
func TestAttentionRaiseRejectsUnconfiguredRepo(t *testing.T) {
	s := newAttentionTestServer(t)
	for _, repo := range []string{"victim/repo", "attacker/anything", "octocat/not-registered"} {
		_, err := s.handleAttentionRaise(context.Background(), raiseParams(t, AttentionRaiseParams{
			Producer: ProducerBudgetCeiling, Repo: repo, Issue: 1, RunID: "run-1",
		}))
		if err == nil {
			t.Errorf("raise against unconfigured repo %q was accepted, want rejection", repo)
		}
	}
	if reqs, _ := s.attentionStore().List(attention.ListFilter{IncludeTerminal: true}); len(reqs) != 0 {
		t.Errorf("rejected raises wrote %d record(s), want 0", len(reqs))
	}
}

// TestBudgetCeilingNumbersAreNotCallerControlled — the second step.
//
// The params type carries no money at all, so the strongest possible statement
// is structural: no field of AttentionRaiseParams can carry a ceiling or a
// cost. The behavioural half follows — a raise that sends those keys anyway
// (they are simply ignored by the decoder) produces the card the daemon's OWN
// state dictates, and its raiseCeiling arg is exactly
// ProposedCeilingUSD(PipelineBudgetCeilingUSD(root), recordedSpend).
func TestBudgetCeilingNumbersAreNotCallerControlled(t *testing.T) {
	typ := reflect.TypeOf(AttentionRaiseParams{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, bad := range []string{"cost", "ceiling", "usd", "budget", "price", "amount"} {
			if strings.Contains(name, bad) {
				t.Errorf("AttentionRaiseParams.%s: no monetary field may cross this wire — "+
					"the raiseCeiling option's arg is persisted verbatim on resolve",
					typ.Field(i).Name)
			}
		}
	}

	const (
		repo      = "octocat/acme"
		issue     = 1
		realSpend = 12.0
	)
	s := newAttentionTestServer(t)
	recordRunSpend(t, s, repo, issue, realSpend)

	// A caller sending the old, now-unknown keys with absurd values.
	raw := json.RawMessage(fmt.Sprintf(
		`{"producer":%q,"repo":%q,"issue":%d,"runId":"run-1","costUsd":0.01,"ceilingUsd":1000000}`,
		ProducerBudgetCeiling, repo, issue))
	res, err := s.handleAttentionRaise(context.Background(), raw)
	if err != nil {
		t.Fatalf("handleAttentionRaise: %v", err)
	}
	if out := res.(AttentionRaiseResult); out.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("outcome = %q, want created", out.Outcome)
	}

	card := onlyOpenRequest(t, s)
	raise := card.FindOption("raise")
	if raise == nil {
		t.Fatalf("no raise option on the card: %+v", card.Options)
	}
	wantCeiling := orchestrator.ProposedCeilingUSD(
		orchestrator.PipelineBudgetCeilingUSD(s.workspaceRoot), realSpend)
	gotCeiling, _ := raise.Args["ceilingUsd"].(float64)
	if gotCeiling != wantCeiling {
		t.Errorf("raiseCeiling arg = %v, want %v (server-derived) — the caller moved the number",
			gotCeiling, wantCeiling)
	}
	if gotCeiling >= 1_000_000 {
		t.Errorf("raiseCeiling arg = %v — the caller's ceiling reached the card", gotCeiling)
	}
	if card.Context.CostSoFarUSD != realSpend {
		t.Errorf("context.cost_so_far_usd = %v, want the RECORDED spend %v",
			card.Context.CostSoFarUSD, realSpend)
	}
}

// TestBudgetCeilingWithoutCorroboratedSpendOffersNoRaise — the third step.
//
// A ceiling stop the daemon has no run record for still gets a card (staying
// silent would reintroduce the hole #305 closes), but WITHOUT the option whose
// resolution writes budget-override.json. Nothing that could execute a
// workspace-wide write is offered on a report nothing corroborates.
func TestBudgetCeilingWithoutCorroboratedSpendOffersNoRaise(t *testing.T) {
	s := newAttentionTestServer(t) // no recorded runtime, no persisted runtime-N.json

	got := mustRaise(t, s, AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: "octocat/acme", Issue: 1, RunID: "run-1",
	})
	if got.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("outcome = %q, want created — the operator must still be told", got.Outcome)
	}
	card := onlyOpenRequest(t, s)
	if card.FindOption("raise") != nil {
		t.Errorf("an uncorroborated ceiling stop offered `raise`: %+v", card.Options)
	}
	for _, o := range card.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q — an uncorroborated card must offer noops only", o.ID, o.Verb)
		}
	}
}

// TestBudgetCeilingSpendComesFromTheRunsOwnRepo — the runtime map is keyed by
// bare issue number (#370's re-keying target), so a live run of repo B's issue
// #7 must not corroborate a raise naming repo A's issue #7.
func TestBudgetCeilingSpendComesFromTheRunsOwnRepo(t *testing.T) {
	s := newAttentionTestServer(t)
	recordRunSpend(t, s, "o/r", 7, 99) // a DIFFERENT repo's run, same issue number

	got := mustRaise(t, s, AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: "octocat/acme", Issue: 7, RunID: "run-1",
	})
	if got.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("outcome = %q, want created", got.Outcome)
	}
	card := onlyOpenRequest(t, s)
	if card.Context.CostSoFarUSD != 0 {
		t.Errorf("cost_so_far_usd = %v, want 0 — another repo's run corroborated this card",
			card.Context.CostSoFarUSD)
	}
	if card.FindOption("raise") != nil {
		t.Errorf("cross-repo runtime corroborated a raiseCeiling offer: %+v", card.Options)
	}
}

// TestBudgetCeilingSpendFallsBackToThePersistedRuntime — the daemon's own
// persisted runtime-{N}.json is an equally valid corroboration source: it is
// written by this process, never by the caller.
func TestBudgetCeilingSpendFallsBackToThePersistedRuntime(t *testing.T) {
	const (
		repo  = "octocat/acme"
		issue = 21
		spend = 40.0
	)
	s := newAttentionTestServer(t)
	dir := s.pipelineStateDir(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`{"repo":%q,"issueNumber":%d,"totalCostUsd":%v}`, repo, issue, spend)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("runtime-%d.json", issue)), []byte(body), 0o644); err != nil {
		t.Fatalf("write runtime: %v", err)
	}

	mustRaise(t, s, AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: repo, Issue: issue, RunID: "run-1",
	})
	card := onlyOpenRequest(t, s)
	if card.Context.CostSoFarUSD != spend {
		t.Errorf("cost_so_far_usd = %v, want the persisted %v", card.Context.CostSoFarUSD, spend)
	}
	wantCeiling := orchestrator.ProposedCeilingUSD(
		orchestrator.PipelineBudgetCeilingUSD(s.workspaceRoot), spend)
	raise := card.FindOption("raise")
	if raise == nil {
		t.Fatalf("persisted corroboration produced no raise option: %+v", card.Options)
	}
	if gotCeiling, _ := raise.Args["ceilingUsd"].(float64); gotCeiling != wantCeiling {
		t.Errorf("raiseCeiling arg = %v, want %v", raise.Args["ceilingUsd"], wantCeiling)
	}
}

// TestAbandonedDispatchCardIsInformationalNotARetry — finding 2. Every card
// this producer raises is a consequence of an operator Stop
// (`forceClearStuckSlots` has one call site: `abortAll`'s deadline branch), so
// the card must not be a blocking_run unblock whose primary action re-dispatches
// the work the operator just cancelled.
func TestAbandonedDispatchCardIsInformationalNotARetry(t *testing.T) {
	card := orchestrator.BuildAbandonedDispatch("octocat/acme", 9, "run-1", "feature-dev")

	if card.Severity != attention.SeverityFYI {
		t.Errorf("severity = %q, want fyi — nothing is blocked by an operator's own Stop", card.Severity)
	}
	if card.Kind != attention.KindApprove {
		t.Errorf("kind = %q, want approve — there is no pipeline action to unblock", card.Kind)
	}
	for _, o := range card.Options {
		if o.Verb != attention.VerbNoop {
			t.Errorf("option %q binds %q — a Stop must never be undone by this card", o.ID, o.Verb)
		}
		if o.Style == attention.StylePrimary {
			t.Errorf("option %q is StylePrimary — this card recommends nothing", o.ID)
		}
	}
	if card.FindOption("retry") != nil {
		t.Error("the card still offers `retry`, which re-dispatches deliberately-cancelled work")
	}
	for _, want := range []string{"stopped the pipeline", "worktree is PRESERVED", "may be stale"} {
		if !strings.Contains(card.Body, want) {
			t.Errorf("body does not mention %q — the card must name the Stop and the cleanup risk", want)
		}
	}
}

// TestAttentionRaiseWithoutStoreIsAnError — an unconfigured store is a FAULT,
// not a fifth outcome. Callers swallow it (fail-open), but they swallow a
// failure, not a decision.
func TestAttentionRaiseWithoutStoreIsAnError(t *testing.T) {
	// The repo IS configured, so the raise reaches the store check rather than
	// stopping at the repo gate — otherwise this test would pass for the wrong
	// reason and stop covering the case it names.
	resolver := NewClientResolver(nil, false)
	resolver.RegisterRepo("o", "r", t.TempDir())
	s := &Server{resolver: resolver}

	_, err := s.handleAttentionRaise(context.Background(), raiseParams(t, AttentionRaiseParams{
		Producer: ProducerAbandonedDispatch, Repo: "o/r", Issue: 1,
	}))
	if err == nil {
		t.Fatal("expected an error when no attention store is configured")
	}
	if !strings.Contains(err.Error(), "attention store not configured") {
		t.Errorf("error = %q, want the store-not-configured fault", err)
	}
}

// TestRaisedCardsMatchCapturedEnvelopeGrammar — #166. The raiseable producers
// must write records in the SAME envelope grammar the shipping store has
// actually been writing, captured from 110 real records by
// scripts/capture-attention-fixture.sh. This is the "no parallel second
// attention system" claim, checked against real data.
//
// It is a two-way check, and the direction that matters is the first one:
//
//  1. Every token the REAL corpus exhibits — schema version, kind, severity,
//     lifecycle state, option verb, journal action, context field name — is a
//     member of the closed Go registry that claims to describe it. That is the
//     assertion real data can make and a hand-written fixture cannot: it fails
//     when the store's actual output has drifted from its own constants.
//  2. Every token the three new builders emit is drawn from those same
//     registries, and their kinds/severities are ones surfaces already render.
//
// What it deliberately does NOT assert is that a new producer's option verbs
// or default_action already appear in the corpus. They cannot: the corpus has
// ZERO run-scoped records (see internal/attention/testdata/README.md) precisely
// because the run-scoped producers never fired on this machine — that absence
// is the defect, not a property to lock in.
func TestRaisedCardsMatchCapturedEnvelopeGrammar(t *testing.T) {
	var corpus struct {
		RecordsScanned int            `json:"records_scanned"`
		JournalActions map[string]int `json:"journal_actions"`
		Envelopes      []struct {
			SchemaVersion  int      `json:"schema_version"`
			Kind           string   `json:"kind"`
			Severity       string   `json:"severity"`
			DefaultAction  string   `json:"default_action"`
			LifecycleState string   `json:"lifecycle_state"`
			ContextFields  []string `json:"context_fields"`
			OptionVerbs    []string `json:"option_verbs"`
		} `json:"envelopes"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "attention", "testdata", "captured-envelopes.json"))
	if err != nil {
		t.Fatalf("read captured fixture: %v", err)
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse captured fixture: %v", err)
	}
	if corpus.RecordsScanned == 0 || len(corpus.Envelopes) == 0 {
		t.Fatal("captured fixture is empty — it must be regenerated from a real store")
	}

	set := func(vals ...string) map[string]bool {
		m := map[string]bool{}
		for _, v := range vals {
			if v != "" {
				m[v] = true
			}
		}
		return m
	}
	// The closed registries the code declares.
	declaredKinds := set(string(attention.KindUnblock), string(attention.KindApprove),
		string(attention.KindChoose), string(attention.KindProvideInput),
		string(attention.KindHandoff), string(attention.KindResume))
	declaredSeverities := set(string(attention.SeverityFYI), string(attention.SeverityBlockingRun),
		string(attention.SeverityBlockingFleet))
	declaredStates := set(string(attention.StateOpen), string(attention.StateAcknowledged),
		string(attention.StateResolved), string(attention.StateExpired), string(attention.StateAutoResolved))
	declaredActions := set(attention.ActionCreated, attention.ActionUpdated, attention.ActionRefreshed,
		attention.ActionAcknowledged, attention.ActionResolved, attention.ActionExpired,
		attention.ActionAutoResolved)
	declaredContextFields := map[string]bool{}
	ctxType := reflect.TypeOf(attention.Context{})
	for i := 0; i < ctxType.NumField(); i++ {
		tag := strings.Split(ctxType.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			declaredContextFields[tag] = true
		}
	}

	// --- direction 1: real output conforms to the declared registries ---
	corpusKinds, corpusSeverities := map[string]bool{}, map[string]bool{}
	for _, e := range corpus.Envelopes {
		if e.SchemaVersion != attention.SchemaVersion {
			t.Fatalf("captured schema_version = %d, code says %d — the envelope moved under the store",
				e.SchemaVersion, attention.SchemaVersion)
		}
		if !declaredKinds[e.Kind] {
			t.Errorf("real records carry kind %q, which is not a declared Kind", e.Kind)
		}
		if !declaredSeverities[e.Severity] {
			t.Errorf("real records carry severity %q, which is not a declared Severity", e.Severity)
		}
		if !declaredStates[e.LifecycleState] {
			t.Errorf("real records carry lifecycle state %q, which is not a declared State", e.LifecycleState)
		}
		for _, verb := range e.OptionVerbs {
			if !attention.IsRegisteredVerb(verb) {
				t.Errorf("real records offer verb %q, which is not in the closed registry", verb)
			}
		}
		for _, f := range e.ContextFields {
			if !declaredContextFields[f] {
				t.Errorf("real records carry context field %q, which attention.Context does not declare", f)
			}
		}
		corpusKinds[e.Kind] = true
		corpusSeverities[e.Severity] = true
	}
	for action := range corpus.JournalActions {
		if !declaredActions[action] {
			t.Errorf("real journal carries action %q, which is not a declared action constant", action)
		}
	}
	// The four Raise outcomes are named from the same vocabulary the real
	// journal exhibits, which is what makes them meaningful to a surface.
	for _, o := range []attention.RaiseOutcome{attention.OutcomeCreated, attention.OutcomeUpdated, attention.OutcomeRefreshed} {
		if corpus.JournalActions[string(o)] == 0 {
			t.Errorf("raise outcome %q never appears as a journal action in %d real records", o, corpus.RecordsScanned)
		}
	}

	// --- direction 2: the new builders emit the same grammar ---
	built := []attention.DecisionRequest{
		orchestrator.BuildBudgetCeilingHit("octocat/acme", 1, "run-1", 10, 20),
		orchestrator.BuildBranchProtectionBlock("octocat/acme", 1, 2, "run-1", "review-not-approved: REVIEW_REQUIRED"),
		orchestrator.BuildAbandonedDispatch("octocat/acme", 1, "run-1", "feature-dev"),
	}
	for _, r := range built {
		if !corpusKinds[string(r.Kind)] {
			t.Errorf("%s: kind %q never appears in %d real records — surfaces have no render case for it",
				r.Producer, r.Kind, corpus.RecordsScanned)
		}
		if !corpusSeverities[string(r.Severity)] {
			t.Errorf("%s: severity %q never appears in %d real records", r.Producer, r.Severity, corpus.RecordsScanned)
		}
		for _, o := range r.Options {
			if !attention.IsRegisteredVerb(o.Verb) {
				t.Errorf("%s: option %q binds unregistered verb %q", r.Producer, o.ID, o.Verb)
			}
		}
		if r.DefaultAction != attention.ExpireNoop && r.FindOption(r.DefaultAction) == nil {
			t.Errorf("%s: default_action %q is not a declared option", r.Producer, r.DefaultAction)
		}
		if r.Context.RunID == "" {
			t.Errorf("%s: context.run_id is empty — this is a RUN-SCOPED producer", r.Producer)
		}
	}
}
