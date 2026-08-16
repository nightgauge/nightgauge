package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatch_routing_advice_test.go — eval-advice consumption on the Go
// dispatch path (#581): opt-in via model_routing.use_eval_recommendations
// (default OFF), applied only on the router-chosen branch, only within the
// stage's routed-tier envelope.

const adviceFixture = `{
  "schema_version": 1,
  "generated_at": "2026-08-16T00:00:00Z",
  "min_samples": 5,
  "min_honest_schema_version": 3,
  "entries": [
    {"job_class": "bugfix", "model_id": "claude-opus-5", "effort": "high", "thinking": "on",
     "backoff": "exact", "samples": 9, "pass_rate": 1, "mean_quality": 92, "mean_cost_usd": 0.4,
     "quality_per_dollar": 230, "advisable": true}
  ]
}`

func writeAdvice(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".nightgauge", "model-evals", "routing-advice.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(adviceFixture), 0o644); err != nil {
		t.Fatal(err)
	}
}

func clearRoutingEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV", "")
	t.Setenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL", "")
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_MODE", "")
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS", "")
}

func TestStageBaseModelIgnoresAdviceByDefault(t *testing.T) {
	clearRoutingEnv(t)
	root := t.TempDir()
	writeAdvice(t, root)

	// Default (key off): the advice file exists but is never consulted — the
	// axis query alone decides. Routed tier sonnet stays sonnet.
	model, explicit := stageBaseModel(root, routing.ModeElevated, state.StageFeatureDev, "sonnet")
	if model != "sonnet" || explicit {
		t.Fatalf("stageBaseModel with advice OFF = (%q, %v), want (sonnet, false)", model, explicit)
	}
}

func TestStageBaseModelAppliesAdviceWhenEnabled(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS", "true")
	root := t.TempDir()
	writeAdvice(t, root)

	// Enabled: advisable evidence names opus, which sits inside the elevated
	// envelope → the routed sonnet is re-picked to opus.
	model, explicit := stageBaseModel(root, routing.ModeElevated, state.StageFeatureDev, "sonnet")
	if model != "opus" || explicit {
		t.Fatalf("stageBaseModel with advice ON = (%q, %v), want (opus, false)", model, explicit)
	}
}

func TestStageBaseModelAdviceStaysInsideTheEnvelope(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS", "true")
	root := t.TempDir()
	writeAdvice(t, root)

	// Efficiency caps at sonnet: the opus advice is outside the envelope, so
	// the axis query's own clamp answers — advice never escapes the clamps.
	model, _ := stageBaseModel(root, routing.ModeEfficiency, state.StageFeatureDev, "sonnet")
	if model != "sonnet" {
		t.Fatalf("stageBaseModel(efficiency) with opus advice = %q, want sonnet", model)
	}
}

func TestStageBaseModelAdviceEnabledWithoutFileIsToday(t *testing.T) {
	clearRoutingEnv(t)
	t.Setenv("NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS", "true")
	root := t.TempDir() // no advice file

	model, _ := stageBaseModel(root, routing.ModeElevated, state.StageFeatureDev, "sonnet")
	if model != "sonnet" {
		t.Fatalf("stageBaseModel enabled, no file = %q, want sonnet (fail-open to declared routing)", model)
	}
}
