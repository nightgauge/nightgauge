package models

// Tests for the orthogonal axis fields (registry-axis-schema, epic #567 /
// #578, spike docs/spikes/568-model-identity-axes.md §2): per-transport
// reachability facts, the effort ladder as data, and rate provenance. All
// additive — the resolution tests in registry_test.go prove Resolve/Get/
// HasTier/band uniqueness are untouched.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestTransportFactsRoundTrip (spike §2.4 step 5): every TransportFacts field
// survives a JSON round trip, and the optional fields genuinely disappear
// under omitempty so a minimal {served} block re-encodes without inventing
// keys the canonical file never wrote.
func TestTransportFactsRoundTrip(t *testing.T) {
	in := 0.34
	out := 1.02
	full := TransportFacts{
		Served:         true,
		Verified:       "2026-08-15",
		Evidence:       "grok models catalog listing, grok CLI 1.0.4",
		Rates:          &Rates{Input: in, Output: out},
		RateProvenance: RateProvenanceMeasured,
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full TransportFacts: %v", err)
	}
	var back TransportFacts
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal full TransportFacts: %v", err)
	}
	if !reflect.DeepEqual(full, back) {
		t.Errorf("TransportFacts did not survive the round trip:\n  in:  %+v\n  out: %+v", full, back)
	}

	minimal, err := json.Marshal(TransportFacts{Served: false})
	if err != nil {
		t.Fatalf("marshal minimal TransportFacts: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(minimal, &keys); err != nil {
		t.Fatalf("re-parse minimal TransportFacts: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("minimal TransportFacts encoded keys %v, want exactly {served}: "+
			"omitempty must drop every optional field", keys)
	}
	if _, ok := keys["served"]; !ok {
		t.Errorf("minimal TransportFacts lost the served key: %v — served has no omitempty "+
			"because false is a positive fact (unreachable), not an empty value", keys)
	}
}

// TestValidateEffortLevels covers the loader-level assert tying the data
// authority (effort_levels in the JSON) to the compiled ladder (EffortOrder):
// membership AND order, plus the missing-key shape (nil).
func TestValidateEffortLevels(t *testing.T) {
	cases := []struct {
		name    string
		levels  []string
		wantErr bool
	}{
		{"exact ladder", []string{"low", "medium", "high", "xhigh", "max"}, false},
		{"missing key (stale registry)", nil, true},
		{"empty", []string{}, true},
		{"missing rung", []string{"low", "medium", "high", "xhigh"}, true},
		{"reordered", []string{"low", "medium", "xhigh", "high", "max"}, true},
		{"extra rung", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateEffortLevels(c.levels)
			if (err != nil) != c.wantErr {
				t.Errorf("validateEffortLevels(%v) error = %v, wantErr %v", c.levels, err, c.wantErr)
			}
		})
	}
}

// TestValidateAxisFields covers the transport-facts asserts: rates without
// provenance is the mandatory-provenance violation, and both the transports
// keyspace and the provenance vocabulary are closed sets.
func TestValidateAxisFields(t *testing.T) {
	rates := &Rates{Input: 1, Output: 2}
	model := func(mutate func(*ModelDescriptor)) []ModelDescriptor {
		m := ModelDescriptor{ID: "test-model", Provider: "other"}
		mutate(&m)
		return []ModelDescriptor{m}
	}
	cases := []struct {
		name    string
		models  []ModelDescriptor
		wantErr bool
	}{
		{"no axis fields at all", model(func(m *ModelDescriptor) {}), false},
		{"valid top-level provenance", model(func(m *ModelDescriptor) {
			m.RateProvenance = RateProvenanceList
		}), false},
		{"invalid top-level provenance", model(func(m *ModelDescriptor) {
			m.RateProvenance = "fixture"
		}), true},
		{"served-only transport", model(func(m *ModelDescriptor) {
			m.Transports = map[string]TransportFacts{TransportCLI: {Served: true}}
		}), false},
		{"transport rates with provenance", model(func(m *ModelDescriptor) {
			m.Transports = map[string]TransportFacts{
				TransportCLI: {Served: true, Rates: rates, RateProvenance: RateProvenanceMeasured},
			}
		}), false},
		{"transport rates WITHOUT provenance", model(func(m *ModelDescriptor) {
			m.Transports = map[string]TransportFacts{TransportCLI: {Served: true, Rates: rates}}
		}), true},
		{"invalid transport provenance value", model(func(m *ModelDescriptor) {
			m.Transports = map[string]TransportFacts{
				TransportCLI: {Served: true, Rates: rates, RateProvenance: "vendor"},
			}
		}), true},
		{"transport key outside cli|api", model(func(m *ModelDescriptor) {
			m.Transports = map[string]TransportFacts{"sdk": {Served: true}}
		}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateAxisFields(c.models)
			if (err != nil) != c.wantErr {
				t.Errorf("validateAxisFields error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// TestRegistryDeclaresEffortLevels proves the shipped canonical file carries
// the ladder as data and that it matches EffortOrder — the same fact mustLoad
// asserts at init, restated here so a drift is a readable failure rather than
// an init panic.
func TestRegistryDeclaresEffortLevels(t *testing.T) {
	var rf registryFile
	if err := json.Unmarshal(canonicalRegistryBytes(t), &rf); err != nil {
		t.Fatalf("parsing canonical registry: %v", err)
	}
	if !reflect.DeepEqual(rf.EffortLevels, EffortOrder) {
		t.Errorf("canonical effort_levels = %v, want %v (EffortOrder)", rf.EffortLevels, EffortOrder)
	}
}

// TestXaiTransportFactsCarryMeasuredCatalogEvidence pins the ONLY measured
// transport facts in the registry (spike #568 §1, evidence key M-cat): the
// grok CLI catalog listing of 2026-08-15. grok-build-0.1's served=false is
// the #532 fact stated as data for the first time — the id exists at the
// provider but the Build CLI's chat proxy rejects it. The api transport is
// deliberately ABSENT on all three: the xai API question is pending (#553)
// and pending stays unexpressed, never guessed.
func TestXaiTransportFactsCarryMeasuredCatalogEvidence(t *testing.T) {
	cases := []struct {
		id     string
		served bool
	}{
		{"grok-4.6", true},
		{"grok-4.5", true},
		{"grok-build-0.1", false},
	}
	for _, c := range cases {
		m, ok := Get(c.id)
		if !ok {
			t.Errorf("%s missing from registry", c.id)
			continue
		}
		cli, ok := m.Transports[TransportCLI]
		if !ok {
			t.Errorf("%s declares no cli transport facts; the catalog listing was measured", c.id)
			continue
		}
		if cli.Served != c.served {
			t.Errorf("%s transports.cli.served = %v, want %v", c.id, cli.Served, c.served)
		}
		if cli.Verified != "2026-08-15" {
			t.Errorf("%s transports.cli.verified = %q, want 2026-08-15 (the M-cat check date)",
				c.id, cli.Verified)
		}
		if cli.Evidence == "" {
			t.Errorf("%s transports.cli carries a verified date but no evidence citation", c.id)
		}
		if _, hasAPI := m.Transports[TransportAPI]; hasAPI {
			t.Errorf("%s declares api transport facts; the xai API transport is pending #553 "+
				"and a pending fact stays unexpressed, never guessed", c.id)
		}
	}
}

// TestTransportAndProvenanceValuesMatchSpikeInventory pins every entry's new
// axis values to the spike's §1 inventory table: which transports carry a
// declared served=true fact, which stay unexpressed (deprecated entries whose
// reachability is unverified, the fixture entry, every xai api cell pending
// #553), and each top-level rate card's provenance.
func TestTransportAndProvenanceValuesMatchSpikeInventory(t *testing.T) {
	type want struct {
		cli        *bool // nil = key absent (unexpressed)
		api        *bool
		provenance string
	}
	yes, no := true, false
	declaredBoth := want{cli: &yes, api: &yes, provenance: RateProvenanceList}
	codexList := want{cli: &yes, provenance: RateProvenanceList}
	unexpressedList := want{provenance: RateProvenanceList}
	subscription := want{cli: &yes, provenance: RateProvenanceSubscription}

	wants := map[string]want{
		"claude-opus-5":             declaredBoth,
		"claude-opus-4-8":           declaredBoth,
		"claude-sonnet-5":           declaredBoth,
		"claude-haiku-4-5-20251001": declaredBoth,
		"claude-fable-5":            declaredBoth,
		"claude-sonnet-4-6":         declaredBoth,
		"claude-opus-4-7":           declaredBoth,
		"claude-opus-4-6":           declaredBoth,
		"gpt-5.6-sol":               codexList,
		"gpt-5.6-terra":             codexList,
		"gpt-5.6-luna":              codexList,
		"gpt-5.5":                   codexList,
		"gpt-5.4":                   codexList,
		"gpt-5.4-mini":              codexList,
		"gpt-5.3-codex-spark":       {cli: &yes, provenance: RateProvenancePlaceholder},
		"gpt-5.2":                   unexpressedList,
		"gpt-5.3-codex":             unexpressedList,
		"gpt-5.1-codex-mini":        unexpressedList,
		"gemini-2.5-pro":            declaredBoth,
		"gemini-2.5-flash":          declaredBoth,
		"gemini-2.0-flash":          unexpressedList,
		"gpt-4o-mini":               subscription,
		"gpt-4o":                    subscription,
		"claude-sonnet-4.5":         subscription,
		"grok-4.6":                  {cli: &yes, provenance: RateProvenanceMeasured},
		"grok-4.5":                  {cli: &yes, provenance: RateProvenanceMeasured},
		"grok-build-0.1":            {cli: &no, provenance: RateProvenanceList},
		"vendor-x-pro":              {provenance: RateProvenancePlaceholder},
	}

	all := All()
	if len(all) != len(wants) {
		t.Fatalf("registry has %d models, expectation table has %d — update the table "+
			"(and the spike-inventory mapping) together", len(all), len(wants))
	}
	for _, m := range all {
		w, ok := wants[m.ID]
		if !ok {
			t.Errorf("%s is not in the expectation table", m.ID)
			continue
		}
		if m.RateProvenance != w.provenance {
			t.Errorf("%s rate_provenance = %q, want %q", m.ID, m.RateProvenance, w.provenance)
		}
		for transport, wantServed := range map[string]*bool{TransportCLI: w.cli, TransportAPI: w.api} {
			facts, has := m.Transports[transport]
			if wantServed == nil {
				if has {
					t.Errorf("%s declares transports.%s; the inventory leaves that cell "+
						"unexpressed", m.ID, transport)
				}
				continue
			}
			if !has {
				t.Errorf("%s declares no transports.%s; the inventory records a fact there",
					m.ID, transport)
				continue
			}
			if facts.Served != *wantServed {
				t.Errorf("%s transports.%s.served = %v, want %v", m.ID, transport, facts.Served, *wantServed)
			}
		}
	}
}

// TestNoTransportOverridesTopLevelRatesYet pins the additive phase's rate
// story: every current rate figure lives in the top-level card (measured
// Build-CLI charges for grok-4.6/grok-4.5 since #570), so no transport block
// carries an overriding card. When #553 records the xai API card this test is
// the one to update — with figures, provenance, and a citation.
func TestNoTransportOverridesTopLevelRatesYet(t *testing.T) {
	for _, m := range All() {
		for transport, facts := range m.Transports {
			if facts.Rates != nil {
				t.Errorf("%s transports.%s carries a rate card; this phase declares none — "+
					"transport cards start with #553 (xai api list prices)", m.ID, transport)
			}
		}
	}
}

// ─── Transport-reachability enforcement (fail-closed-axis-enforcement, #579) ─
//
// #578 (above) landed the FACTS; the tests below pin the ENFORCEMENT that
// consults them at selection: ServedByTransport's known/unexpressed
// distinction, CheckTransportServed as the transport-aware resolution entry
// point selection paths use instead of bare Resolve, and the additive
// semantics (#579 AC4) — a model with no transports fact for the transport in
// question fails OPEN, exactly as it did before #578 added the field at all.

// TestServedByTransportDistinguishesUnexpressedFromUnserved pins the
// three-way read: an explicit true, an explicit false, and the unexpressed
// (absent key, or absent transports map entirely) state a caller must never
// conflate with either explicit fact.
func TestServedByTransportDistinguishesUnexpressedFromUnserved(t *testing.T) {
	grokServed, ok := Resolve("xai", "grok-4.6")
	if !ok {
		t.Fatal("grok-4.6 missing from registry")
	}
	if served, known := grokServed.ServedByTransport(TransportCLI); !known || !served {
		t.Errorf("grok-4.6.ServedByTransport(cli) = (%v, %v), want (true, true)", served, known)
	}

	grokBuild, ok := Resolve("xai", "grok-build-0.1")
	if !ok {
		t.Fatal("grok-build-0.1 missing from registry")
	}
	if served, known := grokBuild.ServedByTransport(TransportCLI); !known || served {
		t.Errorf("grok-build-0.1.ServedByTransport(cli) = (%v, %v), want (false, true)", served, known)
	}

	// vendor-x-pro carries no transports field at all — the unexpressed
	// state, not an implicit unserved.
	vendor, ok := Resolve("other", "vendor-x-pro")
	if !ok {
		t.Fatal("vendor-x-pro missing from registry")
	}
	if served, known := vendor.ServedByTransport(TransportCLI); known {
		t.Errorf("vendor-x-pro.ServedByTransport(cli) = (%v, %v), want known=false (unexpressed, #579 AC4)",
			served, known)
	}

	// A transport key outside the closed set must not fabricate an answer.
	if served, known := grokServed.ServedByTransport("carrier-pigeon"); known {
		t.Errorf("ServedByTransport(carrier-pigeon) = (%v, %v), want known=false", served, known)
	}
}

// TestCheckTransportServedThreeOutcomes pins CheckTransportServed's contract
// end to end: an ordinary miss, a served hit, and an unserved hit that fails
// closed with a *TransportUnreachableError naming provider, model, and
// transport — and whose text reuses the EXISTING terminalkind
// model_unavailable classification (table.json's "invalid model" clause)
// rather than inventing a parallel mechanism (#591, #533).
func TestCheckTransportServedThreeOutcomes(t *testing.T) {
	if _, ok, err := CheckTransportServed("xai", TransportCLI, "totally-made-up"); ok || err != nil {
		t.Errorf("CheckTransportServed(unknown) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	m, ok, err := CheckTransportServed("xai", TransportCLI, "grok-4.6")
	if !ok || err != nil {
		t.Fatalf("CheckTransportServed(grok-4.6) = ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if m.ID != "grok-4.6" {
		t.Errorf("CheckTransportServed(grok-4.6) returned %q", m.ID)
	}

	_, ok, err = CheckTransportServed("xai", TransportCLI, "grok-build-0.1")
	if !ok {
		t.Fatal("CheckTransportServed(grok-build-0.1) ok=false, want true — the model exists in the registry")
	}
	var unreachable *TransportUnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("CheckTransportServed(grok-build-0.1) err = %v (%T), want *TransportUnreachableError", err, err)
	}
	if unreachable.Provider != "xai" || unreachable.Model != "grok-build-0.1" || unreachable.Transport != TransportCLI {
		t.Errorf("TransportUnreachableError = %+v, want {Provider:xai Model:grok-build-0.1 Transport:cli}", unreachable)
	}
	for _, want := range []string{"xai", "grok-build-0.1", "cli"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name provider/model/transport; missing %q in %q", want, err.Error())
		}
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid model") {
		t.Errorf("error must contain the literal phrase \"invalid model\" so it classifies under the "+
			"existing terminalkind model_unavailable rule; got %q", err.Error())
	}
}

// TestCheckTransportServedFailsOpenOnUnexpressedTransport pins #579 AC4: a
// model with no transports fact for the transport in question resolves
// exactly as it did before #578 added the field — served, not blocked. This
// is the additive-enforcement guarantee: adding facts to more models over
// time can only narrow the served set, never retroactively block a model
// nobody has assessed yet.
func TestCheckTransportServedFailsOpenOnUnexpressedTransport(t *testing.T) {
	m, ok, err := CheckTransportServed("other", TransportCLI, "vendor-x-pro")
	if !ok || err != nil {
		t.Fatalf("CheckTransportServed(vendor-x-pro) = ok=%v err=%v, want ok=true err=nil "+
			"(unexpressed transports must fail OPEN)", ok, err)
	}
	if m.ID != "vendor-x-pro" {
		t.Errorf("CheckTransportServed(vendor-x-pro) returned %q", m.ID)
	}
}

// TestServedByTransportHypotheticalUnservedModel pins the #579 regression
// requirement on a model that is NOT deprecated but explicitly declares
// transports.cli.served=false — independent of grok-build-0.1's coincidental
// double-reason case below. Constructed directly (not read from the embedded
// registry) so the assertion holds even if grok-build-0.1's own deprecated
// flag ever changes: the transport fact alone is sufficient to mark a model
// unserved, with no help from `deprecated`.
func TestServedByTransportHypotheticalUnservedModel(t *testing.T) {
	hypothetical := ModelDescriptor{
		ID:       "hypothetical-unserved-model",
		Provider: "xai",
		Transports: map[string]TransportFacts{
			TransportCLI: {Served: false},
		},
	}
	if hypothetical.Deprecated {
		t.Fatal("test setup error: hypothetical model must not be deprecated — this pins the TRANSPORT reason alone")
	}
	served, known := hypothetical.ServedByTransport(TransportCLI)
	if !known || served {
		t.Errorf("hypothetical.ServedByTransport(cli) = (%v, %v), want (false, true)", served, known)
	}
}

// TestGrokBuild01UnselectableForBothReasonsIndependently pins the #579
// invariant directly on the real registry entry: grok-build-0.1 keeps
// deprecated:true for historical cost replay, but its unselectability no
// longer rests on that flag alone — transports.cli.served:false is now an
// INDEPENDENT reason, landed by #578. Both facts must hold so a future edit
// (e.g. clearing `deprecated` for some other purpose) cannot silently make
// the model selectable again.
func TestGrokBuild01UnselectableForBothReasonsIndependently(t *testing.T) {
	m, ok := Resolve("xai", "grok-build-0.1")
	if !ok {
		t.Fatal("grok-build-0.1 missing from registry")
	}
	if !m.Deprecated {
		t.Error("reason 1 (deprecated) must independently hold: grok-build-0.1 must be deprecated:true")
	}
	served, known := m.ServedByTransport(TransportCLI)
	if !known || served {
		t.Error("reason 2 (transport) must independently hold: transports.cli.served must be explicitly false")
	}
}
