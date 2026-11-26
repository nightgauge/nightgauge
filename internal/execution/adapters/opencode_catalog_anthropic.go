package adapters

// openCodeAnthropicModels is every model opencode 2.2.7 lists for the
// anthropic provider of the pinned models catalog
// (opencode-models-catalog.json.gz), mapped to the model id OpenCode sends for
// each. 2.2.7 lists a catalog model under its id, and derives one more entry,
// <id>-<mode>, from each of its experimental modes; it sends that entry under
// the base model's id with a body and a header of its own, so a fast-mode
// entry maps to its base model (ADR-022 § 17).
//
// Derived from the pinned catalog, not maintained by hand.
// TestOpenCodeAnthropicModelsMatchThePinnedCatalog fails when this map and the
// catalog differ and prints the entries to paste here.
// TestOpenCodeAnthropicModelsMatchTheBinary, under the opencode_integration
// build tag, fails when the installed binary lists other anthropic models from
// the pinned catalog. Re-capture the catalog whenever the tested version moves
// (§ 20).
var openCodeAnthropicModels = map[string]string{
	"claude-fable-5":            "claude-fable-5",
	"claude-fable-5-1":          "claude-fable-5-1",
	"claude-fable-5-2":          "claude-fable-5-2",
	"claude-fable-5-2-fast":     "claude-fable-5-2",
	"claude-haiku-4-5":          "claude-haiku-4-5",
	"claude-haiku-4-5-20251001": "claude-haiku-4-5-20251001",
	"claude-haiku-5-5":          "claude-haiku-5-5",
	"claude-opus-4-5":           "claude-opus-4-5",
	"claude-opus-4-5-20251101":  "claude-opus-4-5-20251101",
	"claude-opus-4-6":           "claude-opus-4-6",
	"claude-opus-4-7":           "claude-opus-4-7",
	"claude-opus-4-8":           "claude-opus-4-8",
	"claude-opus-4-8-fast":      "claude-opus-4-8",
	"claude-opus-5":             "claude-opus-5",
	"claude-opus-5-5":           "claude-opus-5-5",
	"claude-opus-5-5-fast":      "claude-opus-5-5",
	"claude-opus-5-fast":        "claude-opus-5",
	"claude-sonnet-4-6":         "claude-sonnet-4-6",
	"claude-sonnet-5":           "claude-sonnet-5",
	"claude-sonnet-5-5":         "claude-sonnet-5-5",
}
