package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

func baseLineageKey() LineageKey {
	return LineageKey{Provider: "anthropic", AccountHash: "acct-a", ModelFamily: "claude-sonnet-5-5",
		EffortClass: "light", Repo: "nightgauge/nightgauge", Issue: 55, RunID: "run-1"}
}

var claudeCaps = adaptercompat.CacheCaps{ImplicitPrefixCache: true,
	CompatibleModelTransitions: adaptercompat.TransitionsNone, ExpirySeconds: 300}

func TestLineage_TransitionTable(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(k *LineageKey)
		idle         time.Duration
		reason       string
		resumeDenied bool
	}{
		{"same everything", func(*LineageKey) {}, time.Minute, "", false},
		{"account change", func(k *LineageKey) { k.AccountHash = "acct-b" }, 0, LineageAccountChange, true},
		{"repo change", func(k *LineageKey) { k.Repo = "nightgauge/other" }, 0, LineageRepoChange, true},
		{"issue change", func(k *LineageKey) { k.Issue = 56 }, 0, LineageIssueChange, true},
		{"provider change", func(k *LineageKey) { k.Provider = "openai" }, 0, LineageProviderChange, true},
		{"model family change", func(k *LineageKey) { k.ModelFamily = "claude-opus-5-5" }, 0, LineageModelFamilyChange, false},
		{"effort class change", func(k *LineageKey) { k.EffortClass = "deep" }, 0, LineageEffortClassChange, false},
		{"expired", func(*LineageKey) {}, 6 * time.Minute, LineageExpired, false},
	}
	prev := baseLineageKey()
	prevID := NextLineageID("", prev, LineageTransition{})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			next := baseLineageKey()
			c.mutate(&next)
			tr := CompareLineage(prev, next, claudeCaps, c.idle)
			if tr.Same != (c.reason == "") || tr.Reason != c.reason {
				t.Fatalf("CompareLineage = %+v, want reason %q", tr, c.reason)
			}
			id := NextLineageID(prevID, next, tr)
			if (id == prevID) != (c.reason == "") {
				t.Errorf("id %s vs prev %s: want a new id exactly when the lineage breaks", id, prevID)
			}
			if got := ResumeAllowed(prev, next); got == c.resumeDenied {
				t.Errorf("ResumeAllowed = %v, want %v", got, !c.resumeDenied)
			}
		})
	}
}

func TestLineage_SameFamilyTransitionKeepsLineage(t *testing.T) {
	caps := claudeCaps
	caps.CompatibleModelTransitions = adaptercompat.TransitionsSameFamily
	prev, next := baseLineageKey(), baseLineageKey()
	prev.ModelFamily, next.ModelFamily = "claude-sonnet-5", "claude-sonnet-5-5"
	if tr := CompareLineage(prev, next, caps, 0); !tr.Same {
		t.Errorf("same_family sonnet-5 → sonnet-5-5: %+v, want same lineage", tr)
	}
	next.ModelFamily = "claude-opus-5-5"
	if tr := CompareLineage(prev, next, caps, 0); tr.Reason != LineageModelFamilyChange {
		t.Errorf("sonnet → opus under same_family: %+v, want model_family_change", tr)
	}
}

func TestLineage_ReturnToEarlierKeyIsANewLineage(t *testing.T) {
	a := baseLineageKey()
	b := a
	b.Provider = "openai"
	idA := NextLineageID("", a, LineageTransition{})
	idB := NextLineageID(idA, b, CompareLineage(a, b, claudeCaps, 0))
	idA2 := NextLineageID(idB, a, CompareLineage(b, a, claudeCaps, 0))
	if idA2 == idA || idA2 == idB {
		t.Errorf("A→B→A ids %s %s %s: the return must not reuse a cold lineage's id", idA, idB, idA2)
	}
}

func TestLineage_IdentityHashSaltIsPrivateAndPerWorkspace(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	desc := identityDescriptor("claude-headless", func(string) string { return "" })
	h1, err := workspaceIdentityHash(d1, desc)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := workspaceIdentityHash(d1, desc)
	h2, _ := workspaceIdentityHash(d2, desc)
	if h1 != again {
		t.Error("hash is not stable within one workspace")
	}
	if h1 == h2 {
		t.Error("two salts gave the same hash for one descriptor")
	}
	info, err := os.Stat(filepath.Join(d1, lineageSaltFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("salt mode %v, want 0600", info.Mode().Perm())
	}
}

func TestLineage_DescriptorCarriesNoCredential(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret-bytes", "CLAUDE_CONFIG_DIR": "/home/u/.claude-work"}
	desc := identityDescriptor("claude-headless", func(k string) string { return env[k] })
	if strings.Contains(desc, "sk-ant-secret-bytes") {
		t.Fatalf("descriptor %q carries the API key", desc)
	}
	if !strings.Contains(desc, "auth=api-key") || !strings.Contains(desc, "/home/u/.claude-work") {
		t.Errorf("descriptor %q does not name auth mode and credential home", desc)
	}
}

// TestLineage_DispatchRecordsHandoffAndHistoryStaysClean drives the
// dispatch-side lineage over two stages: same provider gives one lineage and
// no handoff; a switched adapter gives a second lineage and a provider_change
// handoff. The history record carries neither the account hash nor a session.
func TestLineage_DispatchRecordsHandoffAndHistoryStaysClean(t *testing.T) {
	root := layouttest.Repo(t)
	now := time.Now()

	dispatch := func(rs *state.RuntimeState, stage state.PipelineStage, adapter, model string, at time.Time) {
		dl := computeDispatchLineage(rs, root, "nightgauge/nightgauge", 55, adapter, model, "low", at)
		dl.record(rs, stage, 55, adapter, model, AffinityDecision{PenaltyUSD: 0.42}, false, false, at)
	}

	same := state.NewRuntimeState("nightgauge/nightgauge", 55, "", "run-same")
	dispatch(same, state.StageFeaturePlanning, "claude-headless", "sonnet", now)
	dispatch(same, state.StageFeatureDev, "claude-headless", "sonnet", now.Add(time.Minute))
	if a, b := same.StageLineage(state.StageFeaturePlanning).LineageID, same.StageLineage(state.StageFeatureDev).LineageID; a == "" || a != b {
		t.Errorf("same-provider run: lineage ids %q %q, want one shared id", a, b)
	}
	if len(same.HandoffEvents) != 0 {
		t.Errorf("same-provider run: handoffs %+v, want none", same.HandoffEvents)
	}

	hop := state.NewRuntimeState("nightgauge/nightgauge", 55, "", "run-hop")
	dispatch(hop, state.StageFeaturePlanning, "claude-headless", "sonnet", now)
	dispatch(hop, state.StageFeatureDev, "codex", "sonnet", now.Add(time.Minute))
	a, b := hop.StageLineage(state.StageFeaturePlanning).LineageID, hop.StageLineage(state.StageFeatureDev).LineageID
	if a == "" || b == "" || a == b {
		t.Errorf("adapter switch: lineage ids %q %q, want two", a, b)
	}
	if len(hop.HandoffEvents) != 1 || hop.HandoffEvents[0].Reason != LineageProviderChange ||
		hop.HandoffEvents[0].EstimatedColdStartUSD != 0.42 {
		t.Fatalf("adapter switch: handoffs %+v, want one provider_change at $0.42", hop.HandoffEvents)
	}

	hop.RecordStageModel(state.StageFeatureDev, "sonnet")
	rec := state.NewHistoryWriter(t.TempDir()).BuildV2Record(hop, true, "", state.V2RunInput{}, time.Now())
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	acct := hop.LastLineage().AccountHash
	if acct == "" || strings.Contains(string(data), acct) {
		t.Errorf("history JSON must not carry the account hash %q", acct)
	}
	if !strings.Contains(string(data), `"handoff_events"`) || !strings.Contains(string(data), b) {
		t.Errorf("history JSON lacks the handoff event or lineage id: %s", data)
	}
	if strings.Contains(strings.ToLower(string(data)), "session") {
		t.Errorf("history JSON mentions a session: %s", data)
	}
}
