package models

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func canonicalRegistryBytes(t *testing.T) []byte {
	t.Helper()
	canonicalPath := filepath.Join(
		"..", "..", "packages", "nightgauge-sdk", "src", "eval", "model-registry.json",
	)
	b, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("reading canonical SDK registry: %v", err)
	}
	return b
}

// TestParityWithCanonicalSDKRegistry is the cross-language guard: the Go mirror
// (internal/models/model-registry.json) must carry the same data as the
// canonical SDK source of truth.
//
// Comparison is on the RAW parsed JSON (map[string]any), not on the Go structs.
// That distinction is the whole point. Unmarshalling both sides into
// registryFile silently drops any key the Go structs do not model, so both
// sides lose it identically and compare equal — a field present in the
// canonical file and absent from the mirror passed as "in sync". Verified: with
// the struct comparison, adding `behavior.propensity` to the canonical file
// only, and not to the mirror, still passed. Parsing into `any` keeps every
// key, so drift in a field Go has never heard of now fails here.
//
// Parsing (rather than comparing bytes) still keeps this immune to formatting:
// whitespace, key order, and number spelling all normalize away.
//
// If this fails: run scripts/sync-model-registry.sh after editing the canonical
// file (packages/nightgauge-sdk/src/eval/model-registry.json).
func TestParityWithCanonicalSDKRegistry(t *testing.T) {
	var canonical, mirror any
	if err := json.Unmarshal(canonicalRegistryBytes(t), &canonical); err != nil {
		t.Fatalf("parsing canonical registry: %v", err)
	}
	if err := json.Unmarshal(RawJSON(), &mirror); err != nil {
		t.Fatalf("parsing embedded mirror: %v", err)
	}

	if !reflect.DeepEqual(canonical, mirror) {
		t.Errorf("Go registry mirror has drifted from the canonical SDK registry.\n" +
			"Run scripts/sync-model-registry.sh to re-sync.")
	}
}

// TestGoStructsModelEveryCanonicalField is the other half of the guard above.
// Parity proves the two FILES agree; this proves the Go TYPES can actually
// carry what the files say. A field the SDK schema adds and the Go structs omit
// is invisible to every struct-based check — the binary reads the registry and
// silently sees nothing there — so this round-trips each model through the Go
// types and fails on any key that does not survive.
//
// Round-tripping compares key paths, not values: `omitempty` legitimately drops
// zero values, but it can never drop a key the struct does not declare.
func TestGoStructsModelEveryCanonicalField(t *testing.T) {
	var raw struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(canonicalRegistryBytes(t), &raw); err != nil {
		t.Fatalf("parsing canonical registry: %v", err)
	}
	if len(raw.Models) == 0 {
		t.Fatal("canonical registry has no models — fixture is not in a state that can detect drift")
	}

	var typed registryFile
	if err := json.Unmarshal(canonicalRegistryBytes(t), &typed); err != nil {
		t.Fatalf("parsing canonical registry into Go types: %v", err)
	}
	if len(typed.Models) != len(raw.Models) {
		t.Fatalf("model count mismatch: raw %d, typed %d", len(raw.Models), len(typed.Models))
	}

	for i, rawModel := range raw.Models {
		reencoded, err := json.Marshal(typed.Models[i])
		if err != nil {
			t.Fatalf("re-encoding model %d: %v", i, err)
		}
		var roundTripped map[string]any
		if err := json.Unmarshal(reencoded, &roundTripped); err != nil {
			t.Fatalf("re-parsing model %d: %v", i, err)
		}
		id, _ := rawModel["id"].(string)
		assertKeysSurvive(t, id, "", rawModel, roundTripped)
	}
}

// assertKeysSurvive walks the canonical object and fails for any key missing
// from the round-tripped copy — i.e. any field the Go structs do not model.
func assertKeysSurvive(t *testing.T, modelID, path string, want, got map[string]any) {
	t.Helper()
	for key, wantVal := range want {
		where := key
		if path != "" {
			where = path + "." + key
		}
		gotVal, ok := got[key]
		if !ok {
			t.Errorf("model %q: canonical field %q is not modeled by the Go structs — "+
				"the binary parses the registry and silently loses it. "+
				"Add the field to internal/models/registry.go.", modelID, where)
			continue
		}
		wantObj, wantIsObj := wantVal.(map[string]any)
		gotObj, gotIsObj := gotVal.(map[string]any)
		if wantIsObj && gotIsObj {
			assertKeysSurvive(t, modelID, where, wantObj, gotObj)
		}
	}
}

func TestRegistryIntegrity(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("registry is empty")
	}
	for _, id := range []string{
		"claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5-20251001", "claude-fable-5",
	} {
		if _, ok := Get(id); !ok {
			t.Errorf("expected registry to contain %q", id)
		}
	}
	// A non-Anthropic, provider-neutral entry must exist.
	hasOther := false
	for _, m := range All() {
		if m.Provider != "anthropic" {
			hasOther = true
		}
	}
	if !hasOther {
		t.Error("expected at least one non-Anthropic provider-neutral model")
	}
}

func TestTierResolutionPrefersCurrentModel(t *testing.T) {
	got, ok := Get("sonnet")
	if !ok {
		t.Fatal("sonnet tier did not resolve")
	}
	if got.ID != "claude-sonnet-5" {
		t.Errorf("sonnet tier resolved to %q, want claude-sonnet-5 (current, not deprecated 4.6)", got.ID)
	}
	if got.Deprecated {
		t.Error("tier resolution returned a deprecated model")
	}
}

// ─── Provider-aware resolution (#56) ─────────────────────────────────────────

func TestResolveProviderTierBands(t *testing.T) {
	cases := []struct {
		provider, tier, wantID string
	}{
		{"openai", "haiku", "gpt-5.6-luna"},
		{"openai", "sonnet", "gpt-5.6-terra"},
		{"openai", "opus", "gpt-5.6-sol"},
		{"openai", "fable", "gpt-5.6-sol"},
		{"google", "haiku", "gemini-2.5-flash"},
		{"google", "sonnet", "gemini-2.5-flash"},
		{"google", "opus", "gemini-2.5-pro"},
		{"google", "fable", "gemini-2.5-pro"},
		{"copilot", "haiku", "gpt-4o-mini"},
		{"copilot", "sonnet", "gpt-4o"},
		{"copilot", "opus", "claude-sonnet-4.5"},
		{"anthropic", "sonnet", "claude-sonnet-5"},
	}
	for _, c := range cases {
		got, ok := Resolve(c.provider, c.tier)
		if !ok {
			t.Errorf("Resolve(%s, %s) missed, want %s", c.provider, c.tier, c.wantID)
			continue
		}
		if got.ID != c.wantID {
			t.Errorf("Resolve(%s, %s) = %s, want %s", c.provider, c.tier, got.ID, c.wantID)
		}
	}
}

func TestResolveLocalProvidersHaveNoBands(t *testing.T) {
	// ollama/lm-studio have no registry entries by design: the configured
	// local model serves every band and costs $0 via the unknown default.
	for _, provider := range []string{"ollama", "lm-studio"} {
		for _, tier := range []string{"haiku", "sonnet", "opus", "fable"} {
			if m, ok := Resolve(provider, tier); ok {
				t.Errorf("Resolve(%s, %s) = %s, want miss (local providers have no tier hierarchy)",
					provider, tier, m.ID)
			}
		}
	}
}

func TestGetTierLookupStaysAnthropic(t *testing.T) {
	// Bare tier names are the pipeline's canonical routing currency and must
	// keep resolving to Anthropic models even now that other providers carry
	// the same band names.
	for _, tier := range []string{"haiku", "sonnet", "opus", "fable"} {
		m, ok := Get(tier)
		if !ok {
			t.Fatalf("Get(%s) missed", tier)
		}
		if m.Provider != "anthropic" {
			t.Errorf("Get(%s) = %s (provider %s), want an anthropic model", tier, m.ID, m.Provider)
		}
	}
}

func TestResolveExactIDIsProviderAgnostic(t *testing.T) {
	// Concrete ids are globally unique, so an exact-id lookup resolves no
	// matter which provider the caller asked for.
	m, ok := Resolve("anthropic", "gemini-2.5-pro")
	if !ok || m.Provider != "google" {
		t.Errorf("Resolve(anthropic, gemini-2.5-pro) = %+v ok=%v, want the google entry", m, ok)
	}
}

func TestProviderForAdapter(t *testing.T) {
	cases := map[string]string{
		"claude":          "anthropic",
		"claude-sdk":      "anthropic",
		"claude-headless": "anthropic",
		"codex":           "openai",
		"gemini":          "google",
		"gemini-sdk":      "google",
		"copilot":         "copilot",
		"ollama":          "ollama",
		"lm-studio":       "lm-studio",
		"mystery":         "other",
	}
	for adapter, want := range cases {
		if got := ProviderForAdapter(adapter); got != want {
			t.Errorf("ProviderForAdapter(%s) = %s, want %s", adapter, got, want)
		}
	}
}

func TestBandUniquenessAcrossProviders(t *testing.T) {
	// At most one non-deprecated model per (provider, band): tier resolution
	// must be deterministic. mustLoad panics on violations at init; this
	// asserts the shipped data directly so a drift is a readable failure.
	seen := map[string]string{}
	for _, m := range All() {
		if m.Deprecated {
			continue
		}
		for _, tier := range m.Tiers {
			key := m.Provider + "/" + tier
			if prev, dup := seen[key]; dup {
				t.Errorf("band %s served by both %s and %s", key, prev, m.ID)
			}
			seen[key] = m.ID
		}
	}
}

// ─── Behavior block (#77) ────────────────────────────────────────────────────

// TestActiveAnthropicModelsDeclareBehavior pins the vendor-documented facts the
// overlays reason about. These are the values an overlay reads INSTEAD of
// restating them as prose (ADR 016 §5), so a wrong one propagates silently into
// every rendered skill.
func TestActiveAnthropicModelsDeclareBehavior(t *testing.T) {
	cases := []struct {
		id              string
		thinkingOn      bool
		disableCeiling  string
		effortDefault   string
		maxOutputTokens int
	}{
		{"claude-opus-5", true, "high", "high", 128000},
		{"claude-sonnet-5", true, "", "high", 128000},
		{"claude-fable-5", true, ThinkingDisableNever, "high", 128000},
		{"claude-haiku-4-5-20251001", false, "", "", 64000},
	}
	for _, c := range cases {
		m, ok := Get(c.id)
		if !ok {
			t.Errorf("%s missing from registry", c.id)
			continue
		}
		if m.Behavior == nil {
			t.Errorf("%s declares no behavior block", c.id)
			continue
		}
		if got := m.ThinkingOnByDefault(); got != c.thinkingOn {
			t.Errorf("%s ThinkingOnByDefault() = %v, want %v", c.id, got, c.thinkingOn)
		}
		if got := m.Behavior.ThinkingDisableMaxEffort; got != c.disableCeiling {
			t.Errorf("%s thinking_disable_max_effort = %q, want %q", c.id, got, c.disableCeiling)
		}
		if got := m.EffortDefault(); got != c.effortDefault {
			t.Errorf("%s EffortDefault() = %q, want %q", c.id, got, c.effortDefault)
		}
		if got := m.MaxOutputTokens(); got != c.maxOutputTokens {
			t.Errorf("%s MaxOutputTokens() = %d, want %d", c.id, got, c.maxOutputTokens)
		}
	}
}

// TestFableRejectsDisabledThinkingAtEveryEffort is the case the pre-#77 schema
// could not express. Fable 5 returns a 400 for disabled thinking at ANY effort,
// but the only way to say that was to omit the field — which means the exact
// opposite (unconstrained), so the interlock waved the config through.
//
// The assertion is on the conflict decision at EVERY rung of the ladder, not
// just the top: an off-by-one that only guarded xhigh/max (the Opus 5 shape)
// would still pass a `low` + thinking-disabled config straight into the 400.
func TestFableRejectsDisabledThinkingAtEveryEffort(t *testing.T) {
	m, ok := Get("claude-fable-5")
	if !ok {
		t.Fatal("claude-fable-5 missing from registry")
	}
	if m.Behavior == nil || m.Behavior.ThinkingDisableMaxEffort != ThinkingDisableNever {
		t.Fatalf("fixture is not in the state the bug requires: fable must declare %q, got %+v",
			ThinkingDisableNever, m.Behavior)
	}
	for _, effort := range EffortOrder {
		conflict, maxAllowed := m.ThinkingDisableConflict(effort)
		if !conflict {
			t.Errorf("ThinkingDisableConflict(%q) = false; fable rejects disabled thinking "+
				"at every effort, so this config reaches the provider and 400s", effort)
		}
		if maxAllowed != ThinkingDisableNever {
			t.Errorf("ThinkingDisableConflict(%q) ceiling = %q, want %q — rendering an effort "+
				"level here tells the operator to lower effort, which cannot fix it",
				effort, maxAllowed, ThinkingDisableNever)
		}
	}
}

// TestOpus5CeilingStillBoundedAtHigh guards the other direction: adding the
// "never" branch must not turn Opus 5's `high` ceiling into a blanket refusal.
func TestOpus5CeilingStillBoundedAtHigh(t *testing.T) {
	m, ok := Get("claude-opus-5")
	if !ok {
		t.Fatal("claude-opus-5 missing from registry")
	}
	allowed := map[string]bool{"low": false, "medium": false, "high": false, "xhigh": true, "max": true}
	for effort, wantConflict := range allowed {
		conflict, _ := m.ThinkingDisableConflict(effort)
		if conflict != wantConflict {
			t.Errorf("opus-5 ThinkingDisableConflict(%q) = %v, want %v", effort, conflict, wantConflict)
		}
	}
}

// TestPropensityAccessorsReadRegistryData asserts the accessors return what the
// registry declares — and that the opus-4-8 → opus-5 DELEGATION INVERSION is
// visible in data. That inversion is the reason the axis exists: 4.8
// under-reaches for subagents and 5 over-reaches, so one static skill cannot
// serve both, and an overlay keyed on the wrong direction is worse than none.
func TestPropensityAccessorsReadRegistryData(t *testing.T) {
	opus5, ok := Get("claude-opus-5")
	if !ok {
		t.Fatal("claude-opus-5 missing from registry")
	}
	opus48, ok := Get("claude-opus-4-8")
	if !ok {
		t.Fatal("claude-opus-4-8 missing from registry")
	}

	if got := opus5.VerificationPropensity(); got != PropensityHigh {
		t.Errorf("opus-5 VerificationPropensity() = %q, want %q", got, PropensityHigh)
	}
	if got := opus5.DelegationPropensity(); got != PropensityHigh {
		t.Errorf("opus-5 DelegationPropensity() = %q, want %q", got, PropensityHigh)
	}
	if got := opus5.NarrationPropensity(); got != PropensityHigh {
		t.Errorf("opus-5 NarrationPropensity() = %q, want %q", got, PropensityHigh)
	}
	if got := opus48.DelegationPropensity(); got != PropensityLow {
		t.Errorf("opus-4-8 DelegationPropensity() = %q, want %q", got, PropensityLow)
	}
	if opus5.DelegationPropensity() == opus48.DelegationPropensity() {
		t.Error("opus-5 and opus-4-8 report the same delegation propensity; the documented " +
			"inversion between them is what the axis exists to capture")
	}
}

// TestPropensityDefaultsToNormal is the fail-open contract. Every accessor is
// total: a model with no propensity block, a model with no behavior block at
// all, and the zero descriptor (what an unknown or local model resolves to) all
// read `normal` rather than panicking or returning "".
func TestPropensityDefaultsToNormal(t *testing.T) {
	haiku, ok := Get("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatal("claude-haiku-4-5-20251001 missing from registry")
	}
	if haiku.Behavior == nil || haiku.Behavior.Propensity != nil {
		t.Fatalf("fixture is not in the state the bug requires: haiku must have a behavior "+
			"block with NO propensity, got %+v", haiku.Behavior)
	}

	sonnet, _ := Get("claude-sonnet-5")
	for _, c := range []struct {
		label string
		m     ModelDescriptor
		axis  func(ModelDescriptor) string
	}{
		{"haiku(behavior, no propensity).verification", haiku, ModelDescriptor.VerificationPropensity},
		{"haiku(behavior, no propensity).delegation", haiku, ModelDescriptor.DelegationPropensity},
		{"haiku(behavior, no propensity).narration", haiku, ModelDescriptor.NarrationPropensity},
		{"sonnet(propensity, no delegation axis).delegation", sonnet, ModelDescriptor.DelegationPropensity},
		{"zero descriptor (unknown/local model).verification", ModelDescriptor{}, ModelDescriptor.VerificationPropensity},
		{"zero descriptor (unknown/local model).delegation", ModelDescriptor{}, ModelDescriptor.DelegationPropensity},
		{"zero descriptor (unknown/local model).narration", ModelDescriptor{}, ModelDescriptor.NarrationPropensity},
	} {
		if got := c.axis(c.m); got != PropensityNormal {
			t.Errorf("%s = %q, want %q", c.label, got, PropensityNormal)
		}
	}
}

// TestUnknownModelHasNoThinkingConstraint pins the fail-open path for the
// interlock: local/unknown models have no registry entry, so nothing can
// conflict and local runs are never blocked.
func TestUnknownModelHasNoThinkingConstraint(t *testing.T) {
	var zero ModelDescriptor
	for _, effort := range EffortOrder {
		if conflict, _ := zero.ThinkingDisableConflict(effort); conflict {
			t.Errorf("zero descriptor conflicts at %q; unknown/local models must fail open", effort)
		}
	}
}

// TestAnthropicCacheRatesFollowPublishedMultipliers pins every Anthropic
// entry's cache rates to the vendor's published multipliers off base input:
// cache READ is 0.1x, a 5-minute cache WRITE is 1.25x, and a 1-hour cache
// WRITE is 2.0x. These are not independent numbers to be maintained by hand —
// they are derived, and #358 shipped a formula that bills all three pools, so
// a typo in any one of them silently mis-prices every run on that model.
//
// If a future Anthropic model genuinely deviates from the published
// multipliers, do NOT relax this test: add that id to an explicit exception
// list here carrying the vendor's published per-1M number and a link to the
// pricing page that states it. An exception must be visible and sourced; a
// loosened assertion is neither.
//
// Comparison note: 1.25 and 2.0 are exactly representable in binary floating
// point, so the write rates match their products bit-for-bit. 0.1 is not
// (0.1*3.0 is 0.30000000000000004, while the published Sonnet read rate is
// 0.30), so the check allows a relative epsilon rather than pretending the
// product is exact.
func TestAnthropicCacheRatesFollowPublishedMultipliers(t *testing.T) {
	const (
		cacheReadMultiplier       = 0.1
		cacheCreation5mMultiplier = 1.25
		cacheCreation1hMultiplier = 2.0
	)

	seen := 0
	for _, m := range All() {
		if m.Provider != "anthropic" {
			continue
		}
		seen++
		t.Run(m.ID, func(t *testing.T) {
			for _, c := range []struct {
				pool string
				got  *float64
				want float64
			}{
				{"cache_read", m.Rates.CacheRead, m.Rates.Input * cacheReadMultiplier},
				{"cache_creation_5m", m.Rates.CacheCreation5m, m.Rates.Input * cacheCreation5mMultiplier},
				{"cache_creation_1h", m.Rates.CacheCreation1h, m.Rates.Input * cacheCreation1hMultiplier},
			} {
				if c.got == nil {
					t.Errorf("%s is nil; Anthropic bills every cache pool, so a nil "+
						"rate prices it at $0 and under-reports the bill", c.pool)
					continue
				}
				if !ratesEqual(*c.got, c.want) {
					t.Errorf("%s = %g, want %g (input %g x published multiplier)",
						c.pool, *c.got, c.want, m.Rates.Input)
				}
			}
		})
	}
	if seen == 0 {
		t.Fatal("no anthropic models in the registry; this test asserted nothing")
	}
}

// ratesEqual compares two per-1M rates. Exact first — the 1.25x/2.0x products
// are exact in binary — then a relative epsilon for the 0.1x read rate, whose
// product is not.
func ratesEqual(got, want float64) bool {
	if got == want {
		return true
	}
	return math.Abs(got-want) <= 1e-12*math.Abs(want)
}
