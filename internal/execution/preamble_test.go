package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBehavioralPreambleMatchesEvalVariant pins the Go constant to the
// measured eval variant — the single source of truth for the preamble text.
// If this fails, evals/variants/behavioral-preamble.json changed without
// re-measuring, or the Go copy drifted. Update both together (and the TS
// mirrors in nightgauge-sdk / nightgauge-vscode) only behind a new
// measurement; see docs/spikes/77-*.md.
func TestBehavioralPreambleMatchesEvalVariant(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "evals", "variants", "behavioral-preamble.json"))
	if err != nil {
		t.Fatalf("read behavioral-preamble.json: %v", err)
	}
	var variant struct {
		Prepend string `json:"prepend"`
	}
	if err := json.Unmarshal(data, &variant); err != nil {
		t.Fatalf("parse behavioral-preamble.json: %v", err)
	}
	if variant.Prepend == "" {
		t.Fatal("behavioral-preamble.json has no prepend text")
	}
	if variant.Prepend != BehavioralPreamble {
		t.Errorf("BehavioralPreamble drifted from evals/variants/behavioral-preamble.json .prepend\nGo:   %q\nJSON: %q",
			BehavioralPreamble, variant.Prepend)
	}
}

func TestWithBehavioralPreamble(t *testing.T) {
	const prompt = "# Stage skill body"

	for _, model := range []string{"haiku", "claude-haiku-4-5-20251001"} {
		got := WithBehavioralPreamble(prompt, model)
		want := BehavioralPreamble + "\n\n" + prompt
		if got != want {
			t.Errorf("model %q: expected preamble prepended with blank-line join", model)
		}
		if !strings.HasSuffix(got, prompt) {
			t.Errorf("model %q: original prompt must be preserved verbatim", model)
		}
	}

	// Measured skip: never inject for other tiers (#77).
	for _, model := range []string{"sonnet", "claude-sonnet-5", "opus", "claude-opus-4-8", "claude-fable-5", ""} {
		if got := WithBehavioralPreamble(prompt, model); got != prompt {
			t.Errorf("model %q: prompt must pass through unchanged, got %q", model, got)
		}
	}
}
