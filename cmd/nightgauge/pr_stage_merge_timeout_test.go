package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/github"
)

// TestPrMergeDefaultTimeoutCoversTheWorstCaseRun is #2214, #2835 and #3198:
// the pr-merge verb's default --timeout must outlast one Run that spends a full
// CI wait, the mergeability backoff and the full merge-queue wait, then every
// infrastructure re-enqueue: its delay and another full queue wait.
func TestPrMergeDefaultTimeoutCoversTheWorstCaseRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	retries := func(q time.Duration) time.Duration {
		return config.MergeQueueInfraRetries * (config.MergeQueueInfraRetryDelay + q)
	}
	worst := github.DefaultCIPollInterval*github.DefaultCIPollMax + 126*time.Second +
		config.DefaultMergeQueueWaitTimeout + retries(config.DefaultMergeQueueWaitTimeout)
	got := time.Duration(prMergeDefaultTimeoutSec(t.TempDir())) * time.Second
	if got <= worst {
		t.Errorf("default = %v, does not cover the worst-case run %v", got, worst)
	}
	if want := 307*time.Minute + 6*time.Second; got != want {
		t.Errorf("default = %v, want %v", got, want)
	}

	short := t.TempDir()
	if err := os.MkdirAll(filepath.Join(short, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "owner: acme\nrepo: tiny\npipeline:\n  merge_queue:\n    wait_timeout: 5m\n"
	if err := os.WriteFile(filepath.Join(short, ".nightgauge", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	want := 15*time.Minute + 126*time.Second + 5*time.Minute + retries(5*time.Minute) + config.PRMergeTimeoutMargin
	if got := time.Duration(prMergeDefaultTimeoutSec(short)) * time.Second; got != want {
		t.Errorf("short queue wait = %v, want %v", got, want)
	}
}
