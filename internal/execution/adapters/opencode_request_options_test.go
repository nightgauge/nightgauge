package adapters

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestOpenCodeRequestOptionsPerStage pins request_options: the dispatched
// model's entry carries them as OpenCode model options (which OpenCode
// 1.18.32 merges into the request body), a stage's own entry overrides them
// key by key, and a key that would redirect the request is dropped.
func TestOpenCodeRequestOptionsPerStage(t *testing.T) {
	settings := config.OpenCodeConfig{
		Endpoints: []config.OpenCodeEndpointConfig{{
			ID: "omlx", Provider: "openai-compatible", BaseURL: "http://127.0.0.1:8000/v1",
			Limit: config.OpenCodeLimit{Context: 131072, Output: 32000},
			Models: []config.OpenCodeEndpointModel{{
				ID: "Qwen3.8-27B-4bit",
				RequestOptions: map[string]any{
					"thinking_budget":      4096,
					"chat_template_kwargs": map[string]any{"enable_thinking": true},
					"model":                "elsewhere",
				},
				StageRequestOptions: map[string]map[string]any{
					"feature-dev": {"thinking_budget": 8192},
				},
			}},
		}},
	}
	options := func(stage string) map[string]any {
		t.Helper()
		in, err := OpenCodeConfigInputFor(settings, RunOptions{
			Stage: stage, Model: "omlx/Qwen3.8-27B-4bit", WorktreeDir: goldenWorktree,
		}, goldenRunRoot, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		built, err := BuildOpenCodeConfig(in)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Provider map[string]struct {
				Models map[string]struct {
					Options map[string]any `json:"options"`
				} `json:"models"`
			} `json:"provider"`
		}
		if err := json.Unmarshal([]byte(built.Content), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.Provider["omlx"].Models["Qwen3.8-27B-4bit"].Options
	}
	want := map[string]any{"thinking_budget": float64(4096), "chat_template_kwargs": map[string]any{"enable_thinking": true}}
	if got := options("feature-planning"); !reflect.DeepEqual(got, want) {
		t.Errorf("feature-planning options = %v, want %v", got, want)
	}
	want["thinking_budget"] = float64(8192)
	if got := options("feature-dev"); !reflect.DeepEqual(got, want) {
		t.Errorf("feature-dev options = %v, want %v", got, want)
	}
}
