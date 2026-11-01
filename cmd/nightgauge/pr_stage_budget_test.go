package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func writeMergeQueueWait(t *testing.T, root, wait string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "project:\n  owner: octocat\n  repo: acme-app\n  number: 1\n" +
		"pipeline:\n  merge_queue:\n    wait_timeout: " + wait + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The #2730 defect was a client limit (20m) below the queue wait (90m), which
// killed a healthy wait and paid for an LLM stage. client > cli > queue must
// hold for every configured wait, and each value must move with it.
func TestPrMergeBudget_ClientOutlivesCLIOutlivesQueue(t *testing.T) {
	for _, tc := range []struct {
		wait string
		want time.Duration
	}{
		{"1m", time.Minute},
		{"30m", 30 * time.Minute},
		{"90m", 90 * time.Minute},
		{"3h", 3 * time.Hour},
	} {
		t.Run(tc.wait, func(t *testing.T) {
			root := layouttest.Repo(t)
			writeMergeQueueWait(t, root, tc.wait)
			b := prMergeBudget(root)
			if got := time.Duration(b.QueueWaitMs) * time.Millisecond; got != tc.want {
				t.Fatalf("queue wait = %s, want %s from config", got, tc.want)
			}
			if !(b.ClientTimeoutMs > b.CLITimeoutMs && b.CLITimeoutMs > b.QueueWaitMs) {
				t.Fatalf("want client > cli > queue, got %+v", b)
			}
			if got := b.CLITimeoutMs / 1000; got != int64(prMergeDefaultTimeoutSec(root)) {
				t.Fatalf("cli_timeout_ms = %ds, the verb's real default is %ds", got, prMergeDefaultTimeoutSec(root))
			}
		})
	}
}

func TestPrMergeBudget_DefaultsToTheConfigDefault(t *testing.T) {
	b := prMergeBudget(layouttest.Repo(t))
	if got := time.Duration(b.QueueWaitMs) * time.Millisecond; got != config.DefaultMergeQueueWaitTimeout {
		t.Fatalf("queue wait = %s, want %s", got, config.DefaultMergeQueueWaitTimeout)
	}
}

func TestPrStageBudgetVerb_EmitsTheContract(t *testing.T) {
	root := layouttest.Repo(t)
	writeMergeQueueWait(t, root, "2h")
	var stdout bytes.Buffer
	cmd := prStageCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"budget", "--workdir", root, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pr-stage budget: %v", err)
	}
	var got prStageBudgetJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("parse %q: %v", stdout.String(), err)
	}
	if got != prMergeBudget(root) {
		t.Fatalf("verb = %+v, want %+v", got, prMergeBudget(root))
	}
}

// The runner has to use the wait the budget reports, or the budget is fiction.
func TestNewPrStageMergeRunner_AppliesConfiguredQueueWait(t *testing.T) {
	wait := 3 * time.Hour
	cfg := &config.Config{Pipeline: &config.PipelineConfig{
		MergeQueue: &config.MergeQueueConfig{WaitTimeout: config.YAMLDuration(wait)},
	}}
	if got := newPrStageMergeRunner(cfg).QueueWaitTimeout(); got != wait {
		t.Fatalf("runner queue wait = %s, want %s", got, wait)
	}
}
