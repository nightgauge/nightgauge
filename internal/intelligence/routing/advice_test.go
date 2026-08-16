package routing

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAdviceFile(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(RoutingAdviceRelativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validAdvice = `{
  "schema_version": 1,
  "generated_at": "2026-08-16T00:00:00Z",
  "min_samples": 5,
  "quality_floor": 70,
  "min_honest_schema_version": 3,
  "entries": [
    {"job_class": "bugfix", "model_id": "claude-sonnet-5", "effort": "low", "thinking": "off",
     "backoff": "exact", "samples": 6, "pass_rate": 1, "mean_quality": 85, "mean_cost_usd": 0.05,
     "quality_per_dollar": 1700, "advisable": true},
    {"job_class": "bugfix", "model_id": "claude-opus-5", "effort": "high", "thinking": "on",
     "backoff": "exact", "samples": 6, "pass_rate": 1, "mean_quality": 92, "mean_cost_usd": 0.4,
     "quality_per_dollar": 230, "advisable": true},
    {"job_class": "refactor", "model_id": "claude-fable-5", "effort": "xhigh", "thinking": "on",
     "backoff": "exact", "samples": 2, "pass_rate": 1, "mean_quality": 99, "mean_cost_usd": 1.5,
     "quality_per_dollar": 66, "advisable": false}
  ]
}`

func TestLoadRoutingAdviceFailsOpen(t *testing.T) {
	if _, ok := LoadRoutingAdvice(t.TempDir()); ok {
		t.Fatal("missing advice file must fail open (ok=false)")
	}
	if _, ok := LoadRoutingAdvice(""); ok {
		t.Fatal("empty workspace root must fail open")
	}

	root := t.TempDir()
	writeAdviceFile(t, root, "{ not json")
	if _, ok := LoadRoutingAdvice(root); ok {
		t.Fatal("unparseable advice file must fail open")
	}

	writeAdviceFile(t, root, `{"schema_version": 999, "entries": []}`)
	if _, ok := LoadRoutingAdvice(root); ok {
		t.Fatal("unknown schema_version must fail open")
	}
}

func TestLoadRoutingAdviceParsesTheSdkShape(t *testing.T) {
	root := t.TempDir()
	writeAdviceFile(t, root, validAdvice)
	advice, ok := LoadRoutingAdvice(root)
	if !ok {
		t.Fatal("valid advice file did not load")
	}
	if len(advice.Entries) != 3 || advice.MinSamples != 5 {
		t.Fatalf("unexpected parse: %+v", advice)
	}
	if advice.Entries[0].Effort != "low" || advice.Entries[0].Thinking != "off" {
		t.Fatalf("envelope fields lost in parse: %+v", advice.Entries[0])
	}
}

func TestAdviseBandPicksPerModeWithinEnvelope(t *testing.T) {
	root := t.TempDir()
	writeAdviceFile(t, root, validAdvice)
	advice, _ := LoadRoutingAdvice(root)

	elevated := Envelope(ModeElevated)
	// Maximum-style quality pick: opus wins on quality.
	if band := AdviseBand(advice, ModeMaximum, elevated); band != TierOpus {
		t.Fatalf("AdviseBand(maximum) = %q, want opus", band)
	}
	// Efficiency: cheapest above the quality floor — sonnet.
	if band := AdviseBand(advice, ModeEfficiency, Envelope(ModeEfficiency)); band != TierSonnet {
		t.Fatalf("AdviseBand(efficiency) = %q, want sonnet", band)
	}
}

func TestAdviseBandNeverEscapesTheEnvelope(t *testing.T) {
	root := t.TempDir()
	writeAdviceFile(t, root, validAdvice)
	advice, _ := LoadRoutingAdvice(root)

	// Efficiency envelope caps at sonnet; a quality-driven opus pick must be
	// rejected, not clamped — advice re-picks within the clamps or not at all.
	if band := AdviseBand(advice, ModeMaximum, Envelope(ModeEfficiency)); band != "" {
		t.Fatalf("AdviseBand outside envelope = %q, want \"\"", band)
	}
}

func TestAdviseBandIgnoresSparseEntries(t *testing.T) {
	// Only the advisable:false fable entry exists → no advice: the axis
	// query alone decides (spike §4.3 — sparse evidence is visible, never
	// applied).
	advice := RoutingAdvice{
		SchemaVersion: RoutingAdviceSchemaVersion,
		Entries: []RoutingAdviceEntry{
			{JobClass: "refactor", ModelID: "claude-fable-5", Effort: "xhigh", Thinking: "on",
				Backoff: "exact", Samples: 2, MeanQuality: 99, MeanCostUsd: 1.5, Advisable: false},
		},
	}
	if band := AdviseBand(advice, ModeFrontier, Envelope(ModeFrontier)); band != "" {
		t.Fatalf("AdviseBand(sparse only) = %q, want \"\"", band)
	}
}

func TestAdviseBandUnknownModelYieldsNoAdvice(t *testing.T) {
	advice := RoutingAdvice{
		SchemaVersion: RoutingAdviceSchemaVersion,
		Entries: []RoutingAdviceEntry{
			{JobClass: "bugfix", ModelID: "my-local-model", Backoff: "exact",
				Samples: 20, MeanQuality: 95, MeanCostUsd: 0, Advisable: true},
		},
	}
	// A model outside the band vocabulary cannot be dispatched on the band
	// wire — no advice, no invented band.
	if band := AdviseBand(advice, ModeMaximum, Envelope(ModeElevated)); band != "" {
		t.Fatalf("AdviseBand(unknown model) = %q, want \"\"", band)
	}
}

// TestRoutingAdviceCrossLanguageFixture pins the Go READER to the TS WRITER
// through one committed, TS-GENERATED artifact:
// testdata/routing-advice-crosslang.json is produced by buildRoutingAdvice
// (regen: NIGHTGAUGE_REGEN_ADVICE_FIXTURE=1, see
// packages/nightgauge-sdk/tests/eval/routingAdvice.test.ts, which fails on
// any drift from the committed bytes). Without this, an in-version field
// rename in routingAdvice.ts keeps both suites green while json.Unmarshal
// zero-values the renamed field here and advice goes silently inert.
func TestRoutingAdviceCrossLanguageFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "routing-advice-crosslang.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeAdviceFile(t, root, string(fixture))
	advice, ok := LoadRoutingAdvice(root)
	if !ok {
		t.Fatal("LoadRoutingAdvice failed on the TS-generated fixture")
	}
	if advice.MinSamples != 5 || advice.QualityFloor != 70 || advice.MinHonestSchemaVersion != 3 {
		t.Fatalf("file-level fields did not round-trip: %+v", advice)
	}
	if len(advice.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(advice.Entries))
	}
	// Every field the Go consumption path keys on must be populated — a
	// zero value here is the silent-rename failure this test exists to catch.
	e := advice.Entries[0]
	if e.JobClass != "bugfix" || e.ModelID != "claude-sonnet-5" || e.Effort != "low" ||
		e.Thinking != "off" || e.Backoff != "exact" || e.Samples != 6 ||
		e.PassRate != 1 || e.MeanQuality != 85 || e.MeanCostUsd != 0.05 ||
		e.QualityPerDollar != 1700 || !e.Advisable {
		t.Fatalf("entry fields did not round-trip: %+v", e)
	}
	if sparse := advice.Entries[2]; sparse.Advisable {
		t.Fatalf("sparse entry lost its advisable=false: %+v", sparse)
	}
}
