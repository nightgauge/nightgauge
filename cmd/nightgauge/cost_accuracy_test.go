package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `nightgauge cost accuracy` (#1213).
//
// "Is the estimate getting better?" was answerable only by reading Slack: the
// pre-flight projection lived in the pipeline STATE's pipeline_meta, which is
// discarded when the run ends. The estimate is now on the durable run record
// and this reports over it.

// writeHistory plants JSONL run records where LoadHistory reads them.
func writeHistory(t *testing.T, root string, records []map[string]any) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	name := time.Now().Format("2006-01-02") + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRecord(issue int, size string, estimateUSD, actualUSD float64, source, provider string) map[string]any {
	now := time.Now().Format(time.RFC3339)
	rec := map[string]any{
		"schema_version": "2",
		"record_type":    "run",
		"issue_number":   issue,
		"repo":           "nightgauge/nightgauge",
		"title":          "t",
		"branch":         "b",
		"base_branch":    "main",
		"execution_mode": "automatic",
		"started_at":     now,
		"completed_at":   now,
		"outcome":        "complete",
		"size":           size,
		"stages":         map[string]any{},
		"tokens":         map[string]any{"estimated_cost_usd": actualUSD},
		"files":          map[string]any{},
		"routing":        map[string]any{},
	}
	if source != "" {
		rec["budget_estimate"] = map[string]any{
			"usd": estimateUSD, "source": source, "provider": provider,
		}
	}
	return rec
}

// captureAccuracyJSON runs the verb with --json from root and decodes stdout.
func captureAccuracyJSON(t *testing.T, root string) map[string]any {
	t.Helper()
	t.Chdir(root)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w

	cmd := costAccuracyCmd()
	cmd.SetArgs([]string{"--json"})
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = orig
	if runErr != nil {
		t.Fatalf("cost accuracy: %v", runErr)
	}
	var out map[string]any
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode --json output: %v", err)
	}
	return out
}

func TestCostAccuracy_ReportsRatiosBySizeProviderAndSource(t *testing.T) {
	root := t.TempDir()
	writeHistory(t, root, []map[string]any{
		// Estimated $10, cost $20 — a 2x under-estimate, the observed direction.
		runRecord(1, "M", 10, 20, "static", "anthropic"),
		runRecord(2, "M", 10, 20, "static", "anthropic"),
		runRecord(3, "L", 20, 20, "historical-p75", "xai"),
	})

	out := captureAccuracyJSON(t, root)
	if got := out["runs_paired"]; got != float64(3) {
		t.Errorf("runs_paired = %v, want 3", got)
	}

	rows, _ := out["rows"].([]any)
	find := func(group, key string) map[string]any {
		for _, r := range rows {
			m := r.(map[string]any)
			if m["group"] == group && m["key"] == key {
				return m
			}
		}
		t.Fatalf("no %s row for %q in %v", group, key, rows)
		return nil
	}

	// All three groupings must be present — the three known biases are
	// independent, so one grouping cannot stand in for another.
	if m := find("size", "M"); math.Abs(m["median_ratio"].(float64)-2.0) > 1e-9 {
		t.Errorf("size:M median = %v, want 2.0", m["median_ratio"])
	}
	if m := find("size", "M"); m["verdict"] != "under-estimating" {
		t.Errorf("size:M verdict = %v, want under-estimating", m["verdict"])
	}
	if m := find("provider", "xai"); math.Abs(m["median_ratio"].(float64)-1.0) > 1e-9 {
		t.Errorf("provider:xai median = %v, want 1.0", m["median_ratio"])
	}
	// The 0.8–1.25 band is the SAME one the completion notification renders as
	// "≈ on estimate", so the report and the notification cannot disagree.
	if m := find("provider", "xai"); m["verdict"] != "on estimate" {
		t.Errorf("provider:xai verdict = %v, want on estimate", m["verdict"])
	}
	if m := find("source", "historical-p75"); m["runs"] != float64(1) {
		t.Errorf("source:historical-p75 runs = %v, want 1", m["runs"])
	}
}

func TestCostAccuracy_SkipsRunsWithNoEstimate(t *testing.T) {
	root := t.TempDir()
	writeHistory(t, root, []map[string]any{
		runRecord(1, "M", 10, 20, "static", "anthropic"),
		// A run that predates the fix: an actual, no estimate. Counting it as
		// "estimated at zero" would be a division by zero, or a 0-cost
		// "perfect" prediction.
		runRecord(2, "M", 0, 20, "", ""),
	})

	out := captureAccuracyJSON(t, root)
	if got := out["runs_paired"]; got != float64(1) {
		t.Errorf("runs_paired = %v, want 1 — the un-estimated run must not pair", got)
	}
}

func TestCostAccuracy_CountsUnpricedSeparatelyFromAPairing(t *testing.T) {
	root := t.TempDir()
	writeHistory(t, root, []map[string]any{
		runRecord(1, "M", 10, 20, "static", "anthropic"),
		// No number was published, so there is no ratio — but a corpus that is
		// mostly unpriceable is itself the finding, so it is counted.
		runRecord(2, "M", 0, 20, "unpriced", "ollama"),
	})

	out := captureAccuracyJSON(t, root)
	if got := out["runs_paired"]; got != float64(1) {
		t.Errorf("runs_paired = %v, want 1", got)
	}
	if got := out["runs_unpriced"]; got != float64(1) {
		t.Errorf("runs_unpriced = %v, want 1", got)
	}
}

func TestCostAccuracy_EmptyCorpusDoesNotDivideByZero(t *testing.T) {
	root := t.TempDir()
	writeHistory(t, root, []map[string]any{})

	out := captureAccuracyJSON(t, root)
	if got := out["runs_paired"]; got != float64(0) {
		t.Errorf("runs_paired = %v, want 0", got)
	}
	if rows, _ := out["rows"].([]any); len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}

func TestPercentileOf(t *testing.T) {
	cases := []struct {
		name   string
		sorted []float64
		p      float64
		want   float64
	}{
		{"empty is zero, not NaN", nil, 50, 0},
		{"single value", []float64{4}, 90, 4},
		{"median of an odd count", []float64{1, 2, 3}, 50, 2},
		{"interpolates between neighbours", []float64{1, 2, 3, 4}, 50, 2.5},
		{"p90 near the top", []float64{1, 2, 3, 4, 5}, 90, 4.6},
	}
	for _, c := range cases {
		if got := percentileOf(c.sorted, c.p); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: percentileOf(%v, %v) = %v, want %v", c.name, c.sorted, c.p, got, c.want)
		}
	}
}

func TestAccuracyVerdict_UsesTheNotificationsOwnBand(t *testing.T) {
	// 0.8–1.25 is the band formatCostAccuracyValue already renders as
	// "≈ on estimate"; duplicating a different one here would let the report
	// and the notification disagree about what "accurate" means.
	cases := map[float64]string{
		0.79: "over-estimating",
		0.80: "on estimate",
		1.00: "on estimate",
		1.25: "on estimate",
		1.26: "under-estimating",
		4.40: "under-estimating",
	}
	for ratio, want := range cases {
		if got := accuracyVerdict(ratio); got != want {
			t.Errorf("accuracyVerdict(%v) = %q, want %q", ratio, got, want)
		}
	}
}
