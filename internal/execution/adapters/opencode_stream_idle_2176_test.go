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

// TestOpenCodeRetryBoundBelowIdleBound pins #3305's ordering: a provider
// retry storm is stopped by the retry bound before the idle-stream watchdog
// could, at an endpoint's default header/chunk timeout, and a hosted model,
// which arms no idle watchdog at all, is still bounded.
func TestOpenCodeRetryBoundBelowIdleBound(t *testing.T) {
	if OpenCodeRetryMaxElapsed <= 0 || OpenCodeRetryMaxAttempts <= 0 {
		t.Fatalf("retry bounds %v / %d must be positive", OpenCodeRetryMaxElapsed, OpenCodeRetryMaxAttempts)
	}
	if OpenCodeRetryMaxElapsed >= openCodeDefaultTimeout {
		t.Fatalf("OpenCodeRetryMaxElapsed = %v, want it below the default idle bound %v", OpenCodeRetryMaxElapsed, openCodeDefaultTimeout)
	}
	a := &OpenCodeAdapter{settings: func(string) (config.OpenCodeConfig, error) {
		return config.OpenCodeConfig{Endpoints: []config.OpenCodeEndpointConfig{{
			ID: "mtplx", Provider: "openai-compatible", BaseURL: "http://127.0.0.1:8000/v1",
			Limit: config.OpenCodeLimit{Context: 131072, Output: 8192},
		}}}, nil
	}}
	run := RunOptions{Model: "mtplx/some-model", RunRoot: &RunRoot{Dir: t.TempDir(), Endpoints: []string{"mtplx"}}}
	_, bound, _, ok := a.StreamIdleBound(run)
	if !ok || OpenCodeRetryMaxElapsed >= bound {
		t.Fatalf("idle bound = %v (armed %v), want the retry bound %v below it", bound, ok, OpenCodeRetryMaxElapsed)
	}
}
