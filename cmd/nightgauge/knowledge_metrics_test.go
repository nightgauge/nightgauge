package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/metrics"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

func TestKnowledgeMetricsCmd_JSONOutput(t *testing.T) {
	dir := t.TempDir()

	histDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ts := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	f, err := os.Create(filepath.Join(histDir, "knowledge-events.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enc := json.NewEncoder(f)
	events := []telemetry.Event{
		{Timestamp: ts, Type: telemetry.EventWrite, Stage: "feature-dev", Path: "k/a.md"},
		{Timestamp: ts, Type: telemetry.EventRead, Stage: "feature-dev", Path: "k/a.md"},
	}
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	f.Close()

	cmd := knowledgeMetricsCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"--workdir", dir, "--window", "7", "--stale-days", "30", "--json"})

	// Replace os.Stdout so JSON output is captured.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	if err := cmd.Execute(); err != nil {
		w.Close()
		t.Fatalf("execute: %v", err)
	}
	w.Close()

	var captured bytes.Buffer
	if _, err := captured.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	var result metrics.Result
	if err := json.Unmarshal(captured.Bytes(), &result); err != nil {
		t.Fatalf("parse json: %v\n%s", err, captured.String())
	}
	if result.WindowDays != 7 {
		t.Errorf("WindowDays = %d; want 7", result.WindowDays)
	}
	if result.Totals.Writes != 1 || result.Totals.Reads != 1 {
		t.Errorf("totals = %+v", result.Totals)
	}
	if result.Status != metrics.StatusEnabled {
		t.Errorf("Status = %s; want enabled", result.Status)
	}
}

func TestKnowledgeMetricsCmd_MissingFile(t *testing.T) {
	dir := t.TempDir()
	cmd := knowledgeMetricsCmd()
	cmd.SetArgs([]string{"--workdir", dir, "--window", "7", "--json"})

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	if err := cmd.Execute(); err != nil {
		w.Close()
		t.Fatalf("execute: %v", err)
	}
	w.Close()

	var captured bytes.Buffer
	captured.ReadFrom(r)

	var result metrics.Result
	if err := json.Unmarshal(captured.Bytes(), &result); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if result.Status != metrics.StatusEmpty {
		t.Errorf("missing file should produce StatusEmpty; got %s", result.Status)
	}
}
