package adapters

// openCodeAnthropicModels is every model the anthropic provider of the catalog
// bundled in opencode 1.18.30 lists, as `opencode models anthropic --verbose`
// prints them with the model fetch off, as in a run, mapped to the model id
// OpenCode sends for each (its api.id). Two entries are not models of their
// own: OpenCode derives a fast-mode entry from a base model, and sends it under
// the base model's id with options and a header of its own (ADR-022 § 17).
//
// Read from the binary, not maintained by hand.
// TestOpenCodeAnthropicModelsMatchTheBinary, under the opencode_integration
// build tag, fails when the installed binary lists other models or sends one
// under another id, and prints the entries to paste here; re-read it whenever
// the tested version moves (§ 20).
var openCodeAnthropicModels = map[string]string{
	"claude-fable-5":             "claude-fable-5",
	"claude-fable-5-1":           "claude-fable-5-1",
	"claude-haiku-4-5":           "claude-haiku-4-5",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5-20251001",
	"claude-opus-4-5":            "claude-opus-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5-20251101",
	"claude-opus-4-6":            "claude-opus-4-6",
	"claude-opus-4-7":            "claude-opus-4-7",
	"claude-opus-4-8":            "claude-opus-4-8",
	"claude-opus-4-8-fast":       "claude-opus-4-8",
	"claude-opus-5":              "claude-opus-5",
	"claude-opus-5-fast":         "claude-opus-5",
	"claude-sonnet-4-5":          "claude-sonnet-4-5",
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5-20250929",
	"claude-sonnet-4-6":          "claude-sonnet-4-6",
	"claude-sonnet-5":            "claude-sonnet-5",
}
