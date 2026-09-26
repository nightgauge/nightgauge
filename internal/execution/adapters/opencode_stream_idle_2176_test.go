package adapters

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestStreamIdleBound_DeclaredEndpointOnly pins #2176's hook: the watchdog
// bound is the larger of the endpoint's header and chunk timeouts, only for a
// model on an endpoint the run declared.
func TestStreamIdleBound_DeclaredEndpointOnly(t *testing.T) {
	a := &OpenCodeAdapter{settings: func(string) (config.OpenCodeConfig, error) {
		return config.OpenCodeConfig{Endpoints: []config.OpenCodeEndpointConfig{{
			ID: "mtplx", Provider: "openai-compatible", BaseURL: "http://127.0.0.1:8000/v1",
			Limit:    config.OpenCodeLimit{Context: 131072, Output: 8192},
			Timeouts: config.OpenCodeTimeouts{Header: config.YAMLDuration(20 * time.Minute), Chunk: config.YAMLDuration(15 * time.Minute)},
		}}}, nil
	}}
	root := t.TempDir()
	run := RunOptions{Model: "mtplx/some-model", RunRoot: &RunRoot{Dir: root, Endpoints: []string{"mtplx"}}}
	ep, bound, db, ok := a.StreamIdleBound(run)
	if !ok || ep != "mtplx" || bound != 20*time.Minute || db != filepath.Join(root, "data", "opencode") {
		t.Fatalf("StreamIdleBound = %q %v %q %v", ep, bound, db, ok)
	}
	for name, r := range map[string]RunOptions{
		"hosted model": {Model: "anthropic/claude-x", RunRoot: run.RunRoot},
		"no run root":  {Model: "mtplx/some-model"},
		"undeclared":   {Model: "mtplx/some-model", RunRoot: &RunRoot{Dir: root}},
	} {
		if _, _, _, ok := a.StreamIdleBound(r); ok {
			t.Errorf("%s: armed, want no watchdog", name)
		}
	}
}
