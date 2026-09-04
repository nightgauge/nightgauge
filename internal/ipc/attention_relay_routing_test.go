package ipc

// Two daemons, one machine (#1421, ADR-019).
//
// Every other attention test in this package stands up ONE server. That is
// structurally blind to the defect this file covers: the platform addresses a
// relayed resolve to an agent identity that is per-MACHINE, while the attention
// store is per-WORKSPACE, so on a machine running daemons for several
// workspaces the command can be handed to a daemon that does not hold the card.
// A single-daemon test always delivers the command to the owner and therefore
// always passes.
//
// WHAT THESE TESTS PROVE: a non-owning daemon classifies the command as
// misrouted rather than rejected, writes nothing — not the resolution, not the
// verb's side effect — under either workspace, and still acknowledges it.
//
// WHAT THEY DO NOT PROVE: that the resolve reaches the owner. It does not.
// Routing needs a platform-side change and is deferred (ADR-019 § Deferred).
// These tests pin the containment and the diagnosis, which are the whole of
// what this repository can decide on its own.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// budgetOverrideRel is the verb's observable side effect: resolving a
// budget-ceiling card with `raise` writes it under the CARD's repo root.
var budgetOverrideRel = filepath.Join(".nightgauge", "pipeline", "budget-override.json")

// twoDaemons builds two independent daemons rooted at two different workspaces,
// each with its own attention store, and both serving the same repo slug — the
// #1421 shape, where a repo slug cannot disambiguate which workspace owns a card.
func twoDaemons(t *testing.T) (owner, other *Server) {
	t.Helper()
	owner = newAttentionTestServer(t)
	other = newAttentionTestServer(t)
	if owner.workspaceRoot == other.workspaceRoot {
		t.Fatalf("both daemons rooted at %s — the test cannot see a cross-workspace misroute", owner.workspaceRoot)
	}
	if owner.attentionStore().Dir() == other.attentionStore().Dir() {
		t.Fatalf("both daemons share attention dir %s", owner.attentionStore().Dir())
	}
	return owner, other
}

// raiseResolvableCard raises a corroborated budget-ceiling card on s and returns
// it with an option whose verb has a side effect worth containing.
func raiseResolvableCard(t *testing.T, s *Server, repo string, issue int) (attention.DecisionRequest, string) {
	t.Helper()
	recordRunSpend(t, s, repo, issue, 80)
	mustRaise(t, s, AttentionRaiseParams{
		Producer: ProducerBudgetCeiling, Repo: repo, Issue: issue, RunID: "run-1",
	})
	card := onlyOpenRequest(t, s)
	if card.FindOption("raise") == nil {
		t.Fatalf("card carries no `raise` remedy, so the verb's side effect cannot be observed: %+v", card.Options)
	}
	return card, "raise"
}

// TestRelayedResolveIsNotAppliedByADaemonThatDoesNotOwnTheCard is AC1/AC2's
// containment half: the non-owner must not apply the resolution, and must not
// silently report a rejection when the real fault is addressing.
func TestRelayedResolveIsNotAppliedByADaemonThatDoesNotOwnTheCard(t *testing.T) {
	ctx := context.Background()
	daemonA, daemonB := twoDaemons(t)
	card, optionID := raiseResolvableCard(t, daemonA, "octocat/acme", 7)

	beforeA := dirFingerprint(t, daemonA.attentionStore().Dir())

	outcome, err := daemonB.ApplyRelayedResolve(ctx, card.ID, optionID, "octocat", "")
	if err == nil {
		t.Fatalf("daemon B resolved a card it does not hold: %+v", outcome)
	}
	if !errors.Is(err, platform.ErrRelayedRequestNotHere) {
		t.Errorf("error = %v, want it to wrap platform.ErrRelayedRequestNotHere — "+
			"a misrouted command must be distinguishable from a rejected option", err)
	}
	if !errors.Is(err, attention.ErrRequestNotFound) {
		t.Errorf("error = %v, want it to still wrap attention.ErrRequestNotFound", err)
	}
	if !outcome.NotInThisWorkspace {
		t.Errorf("outcome.NotInThisWorkspace = false, want true")
	}
	if outcome.Applied || outcome.AlreadyResolved {
		t.Errorf("outcome = %+v, want neither applied nor already-resolved", outcome)
	}

	// Containment: B wrote nothing into A's store...
	if after := dirFingerprint(t, daemonA.attentionStore().Dir()); after != beforeA {
		t.Errorf("daemon B mutated A's store — cross-workspace write\n before: %s\n  after: %s", beforeA, after)
	}
	// ...nor did it materialize the card under its own root...
	if _, err := os.Stat(filepath.Join(daemonB.attentionStore().Dir(), card.ID+".json")); err == nil {
		t.Errorf("daemon B created %s.json in its OWN store — a misroute must not conjure a card", card.ID)
	}
	// ...and the verb never ran on either side.
	for name, root := range map[string]string{"A": daemonA.workspaceRoot, "B": daemonB.workspaceRoot} {
		if _, err := os.Stat(filepath.Join(root, budgetOverrideRel)); err == nil {
			t.Errorf("the `raise` verb's side effect landed under workspace %s on a misrouted command", name)
		}
	}
	// The card is untouched and still waiting for its owner.
	if still := onlyOpenRequest(t, daemonA); still.Lifecycle.Resolved != nil {
		t.Errorf("A's card was resolved by B: %+v", still.Lifecycle.Resolved)
	}
}

// TestMisroutedRelayedResolveIsStillAcknowledgedOnce pins the ack decision
// deliberately rather than by accident. Declining the ack would redeliver onto
// the SAME per-machine agent channel that just misrouted the command, which is
// an unbounded retry and not a fix; the platform-side routing change is the fix
// (ADR-019 § Deferred). If a future change makes the ack conditional, this test
// is the one that must be argued with.
func TestMisroutedRelayedResolveIsStillAcknowledgedOnce(t *testing.T) {
	ctx := context.Background()
	daemonA, daemonB := twoDaemons(t)
	card, optionID := raiseResolvableCard(t, daemonA, "octocat/acme", 7)

	acks := 0
	consumer := platform.NewAttentionCommandConsumer(daemonB,
		func(context.Context, string, string) (string, error) { acks++; return "", nil },
		"agent-1")

	payload, err := json.Marshal(platform.AttentionResolvePayload{
		RequestID: card.ID, OptionID: optionID, Actor: "octocat",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	outcome, err := consumer.Consume(ctx, platform.PendingCommand{
		ID: "cmd-1", Type: platform.AttentionResolveCommandType, Payload: payload,
	})
	if err != nil {
		t.Fatalf("Consume returned a transport error for a misroute: %v", err)
	}
	if acks != 1 {
		t.Errorf("ack count = %d, want exactly 1", acks)
	}
	if !outcome.NotInThisWorkspace {
		t.Errorf("outcome.NotInThisWorkspace = false — the consumer collapsed a misroute into a bare rejection")
	}
	if outcome.Applied {
		t.Errorf("outcome.Applied = true on a card daemon B does not hold")
	}
}

// TestTheOwningDaemonAppliesTheSameRelayedResolve is the control. Without it,
// the two tests above pass for a card that was never resolvable at all, and
// prove nothing about routing. Same card, same option, same actor — only the
// daemon differs, which is the whole of #1421.
func TestTheOwningDaemonAppliesTheSameRelayedResolve(t *testing.T) {
	ctx := context.Background()
	daemonA, daemonB := twoDaemons(t)
	card, optionID := raiseResolvableCard(t, daemonA, "octocat/acme", 7)

	if _, err := daemonB.ApplyRelayedResolve(ctx, card.ID, optionID, "octocat", ""); err == nil {
		t.Fatalf("daemon B applied it — the control cannot distinguish routing from validity")
	}

	outcome, err := daemonA.ApplyRelayedResolve(ctx, card.ID, optionID, "octocat", "")
	if err != nil {
		t.Fatalf("the OWNING daemon failed to apply the same command: %v", err)
	}
	if !outcome.Applied {
		t.Errorf("outcome = %+v, want Applied", outcome)
	}
	if outcome.NotInThisWorkspace {
		t.Errorf("the owner reported NotInThisWorkspace for its own card")
	}
	if _, err := os.Stat(filepath.Join(daemonA.workspaceRoot, budgetOverrideRel)); err != nil {
		t.Errorf("the verb's side effect is missing under the owner's root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(daemonB.workspaceRoot, budgetOverrideRel)); err == nil {
		t.Errorf("the owner's verb wrote under the OTHER workspace's root")
	}
}
