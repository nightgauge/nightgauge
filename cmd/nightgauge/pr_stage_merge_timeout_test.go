package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrMergeDefaultTimeoutCoversTheMergeQueueWait is #2214: the pr-merge
// verb's default --timeout must not cut a merge-queue wait short.
func TestPrMergeDefaultTimeoutCoversTheMergeQueueWait(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got, want := prMergeDefaultTimeoutSec(t.TempDir()), (90+10)*60; got != want {
		t.Errorf("default = %ds, want %ds (90m queue wait + 10m)", got, want)
	}

	short := t.TempDir()
	if err := os.MkdirAll(filepath.Join(short, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "owner: acme\nrepo: tiny\npipeline:\n  merge_queue:\n    wait_timeout: 5m\n"
	if err := os.WriteFile(filepath.Join(short, ".nightgauge", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := prMergeDefaultTimeoutSec(short); got != prMergeCITimeoutSec {
		t.Errorf("short queue wait = %ds, want the CI-wait floor %ds", got, prMergeCITimeoutSec)
	}
}
