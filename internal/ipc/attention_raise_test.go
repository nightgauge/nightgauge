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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

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
		repo    = "octocat/acme"
		issue   = 4242
		runID   = "019f0000-0000-7000-8000-000000000001"
		cost    = 12.50
		ceiling = 10.00
	)
	s := newAttentionTestServer(t)

	got := mustRaise(t, s, AttentionRaiseParams{
		Producer:   ProducerBudgetCeiling,
		Repo:       repo,
		Issue:      issue,
		RunID:      runID,
		CostUSD:    cost,
		CeilingUSD: ceiling,
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
	p := AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: "octocat/acme", Issue: 7,
		RunID: "run-1", CostUSD: 20, CeilingUSD: 15,
	}

	first := mustRaise(t, s, p)
	if first.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("first outcome = %q, want created", first.Outcome)
	}

	p.CostUSD = 25 // the run kept spending; same condition, same key
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

// TestAttentionRaiseOutcomesAreFourDistinctValues — created / refreshed /
// suppressed are three different facts and must not share a return value. A
// caller that cannot tell "a card is on screen" from "the operator already
// dismissed this exact condition" has no basis for deciding whether to also
// toast.
func TestAttentionRaiseOutcomesAreFourDistinctValues(t *testing.T) {
	s := newAttentionTestServer(t)
	// abandoned-dispatch is STANDING with a constant fingerprint, so it can
	// exercise refresh and suppression; budget-ceiling (event-shaped) covers
	// created/updated in the dedup test above.
	p := AttentionRaiseParams{
		Producer: ProducerAbandonedDispatch, Repo: "octocat/acme", Issue: 9,
		RunID: "run-1", Stage: "feature-dev",
	}

	created := mustRaise(t, s, p)
	if created.Outcome != string(attention.OutcomeCreated) {
		t.Fatalf("first outcome = %q, want created", created.Outcome)
	}

	refreshed := mustRaise(t, s, p)
	if refreshed.Outcome != string(attention.OutcomeRefreshed) {
		t.Errorf("re-observed standing outcome = %q, want refreshed (silent)", refreshed.Outcome)
	}

	// The operator resolves it, then the wedge recurs unchanged: ADR-015 §M
	// says do not hand it straight back.
	if _, err := s.attentionStore().Resolve(context.Background(), created.ID, "leave", "octocat", "", "", s); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	before, _ := filepath.Glob(filepath.Join(s.attentionStore().Dir(), "dr_*.json"))

	suppressed := mustRaise(t, s, p)
	if suppressed.Outcome != string(attention.OutcomeSuppressed) {
		t.Errorf("re-raise after resolve outcome = %q, want suppressed", suppressed.Outcome)
	}
	if suppressed.ID != created.ID {
		t.Errorf("suppressed id = %q, want the resolved card's id %q", suppressed.ID, created.ID)
	}
	after, _ := filepath.Glob(filepath.Join(s.attentionStore().Dir(), "dr_*.json"))
	if len(after) != len(before) {
		t.Errorf("suppressed raise wrote a file: %d records before, %d after", len(before), len(after))
	}
	// Open inbox is empty: suppression must not resurrect the resolved card.
	open, _ := s.attentionStore().List(attention.ListFilter{})
	if len(open) != 0 {
		t.Errorf("open requests after suppression = %d, want 0", len(open))
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
		{"budget-ceiling without cost", AttentionRaiseParams{Producer: ProducerBudgetCeiling, Repo: "o/r", Issue: 1, CeilingUSD: 5}},
		{"budget-ceiling without ceiling", AttentionRaiseParams{Producer: ProducerBudgetCeiling, Repo: "o/r", Issue: 1, CostUSD: 5}},
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

// TestAttentionRaiseWithoutStoreIsAnError — an unconfigured store is a FAULT,
// not a fifth outcome. Callers swallow it (fail-open), but they swallow a
// failure, not a decision.
func TestAttentionRaiseWithoutStoreIsAnError(t *testing.T) {
	s := &Server{}
	_, err := s.handleAttentionRaise(context.Background(), raiseParams(t, AttentionRaiseParams{
		Producer: ProducerAbandonedDispatch, Repo: "o/r", Issue: 1,
	}))
	if err == nil {
		t.Fatal("expected an error when no attention store is configured")
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
