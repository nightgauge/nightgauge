package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
)

func TestLearnTune_TunesMeasuredSizeCorpus(t *testing.T) {
	root := t.TempDir()
	recorder := learning.NewRecorder(root)
	for i, actual := range []string{"small", "small", "medium", "large"} {
		if err := recorder.Record(learning.Outcome{
			IssueNumber: i + 1, Repo: "acme/widget",
			PredictedSize: "small", ActualSize: actual,
			Success: true, CompletedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	cmd := learnTuneCmd()
	cmd.SetArgs([]string{"--workdir", root})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("learn tune: %v", err)
		}
	})
	var result struct {
		Tuning struct {
			Param string `json:"param"`
		} `json:"tuning"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if result.Tuning.Param != "size_accuracy" {
		t.Fatalf("tuning.param = %q, want size_accuracy", result.Tuning.Param)
	}
	audit := filepath.Join(root, ".nightgauge", "pipeline", "history", "tuning-audit.jsonl")
	if info, err := os.Stat(audit); err != nil || info.Size() == 0 {
		t.Fatalf("tuning audit was not written: info=%v err=%v", info, err)
	}
}
