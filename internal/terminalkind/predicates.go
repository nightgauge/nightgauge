package terminalkind

import (
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// predicates are the named checks a table term may reference as `@name`.
//
// A predicate exists only for a condition that CANNOT be expressed as literal
// containment — today exactly one, because "does this text name a model?"
// depends on the model registry rather than on any fixed string. Everything
// else is a literal in table.json, where it is visible to review and to the
// generated TypeScript.
//
// Every predicate here must have a TypeScript twin in the SDK classifier, and
// the table declares probes_true / probes_false for it that BOTH sides assert.
// A predicate that answered differently in the two languages would be a silent
// divergence inside a rule that otherwise cannot drift.
var predicates = map[string]func(string) bool{
	"mentions_registry_model": mentionsRegistryModel,
}

// mentionsRegistryModel reports whether the (already lowercased) text names a
// model from the model registry — by concrete ID ("claude-opus-5"), display
// name ("opus 5"), or tier ("opus"/"sonnet"/…). Registry-derived rather than
// hardcoded so new models are covered as the registry evolves (#42).
//
// The TypeScript twin iterates MODEL_REGISTRY, which is the SAME DATA: the SDK
// file packages/nightgauge-sdk/src/eval/model-registry.json is canonical and
// internal/models/model-registry.json is a byte copy, with
// internal/models/registry_test.go failing on drift.
//
// The body is a bare delegation on purpose, and TestPredicateWrapperIsAPure
// Delegation asserts it: everything that reads a registry FIELD lives in
// mentionsAnyModel below, where the shared poison fixture can see it.
func mentionsRegistryModel(t string) bool {
	return mentionsAnyModel(models.All(), t)
}

// mentionsAnyModel is THE FIELD SURFACE of the predicate, and the reason it
// takes the registry rather than reading it.
//
// The literal fences bound what this file may COMPARE against; they say nothing
// about which registry fields it may READ, and a widening needs no literal at
// all. Adding `strings.Contains(t, strings.ToLower(m.Provider))` here would make
// every clause gated on @mentions_registry_model fire for any text that merely
// names a vendor — `Anthropic API: this model is not available on your current
// plan` would become model_unavailable — with no string literal, no golden
// movement and no corpus row able to see it.
//
// So the read set is pinned as an EXACT SET, in both languages, from one shared
// fixture: testdata/predicate-registry-poison.json declares a synthetic model
// whose every non-read field carries a unique sentinel, and the two suites
// assert that no sentinel makes this fire while every declared read field does.
// Taking the registry as a parameter is what lets Go point it at that fixture;
// the SDK twin reaches the same place by mocking the registry module.
//
// It may read: ID, DisplayName, Tiers. Nothing else.
func mentionsAnyModel(ms []models.ModelDescriptor, t string) bool {
	seenTiers := map[string]bool{}
	for _, m := range ms {
		if m.ID != "" && strings.Contains(t, strings.ToLower(m.ID)) {
			return true
		}
		if m.DisplayName != "" && strings.Contains(t, strings.ToLower(m.DisplayName)) {
			return true
		}
		for _, tier := range m.Tiers {
			seenTiers[strings.ToLower(tier)] = true
		}
	}
	for tier := range seenTiers {
		if strings.Contains(t, tier) {
			return true
		}
	}
	return false
}
