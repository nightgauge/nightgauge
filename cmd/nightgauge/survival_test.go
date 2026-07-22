package main

import (
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
)

func TestSurvivalListCommand(t *testing.T) {
	dir := t.TempDir()
	store := survival.NewStore(dir)
	if _, err := store.Append(survival.NewPending("nightgauge/nightgauge", 4151, 4200, "sha-list", "2026-06-01T12:00:00Z", "main")); err != nil {
		t.Fatal(err)
	}

	cmd := survivalListCmd()
	cmd.SetArgs([]string{"--workdir", dir})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	var recs []survival.Record
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("parse output: %v (out=%q)", err, out)
	}
	if len(recs) != 1 || recs[0].MergeCommitSHA != "sha-list" {
		t.Errorf("unexpected records: %+v", recs)
	}
}

func TestSurvivalListVerdictFilter(t *testing.T) {
	dir := t.TempDir()
	store := survival.NewStore(dir)
	if _, err := store.Append(survival.NewPending("nightgauge/r", 1, 1, "p", "2026-06-01T12:00:00Z", "main")); err != nil {
		t.Fatal(err)
	}
	done := survival.NewPending("nightgauge/r", 2, 2, "d", "2026-06-01T12:00:00Z", "main")
	done.Verdict = survival.Reverted
	if _, err := store.Append(done); err != nil {
		t.Fatal(err)
	}

	cmd := survivalListCmd()
	cmd.SetArgs([]string{"--workdir", dir, "--verdict", "reverted"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	var recs []survival.Record
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].Verdict != survival.Reverted {
		t.Errorf("verdict filter failed: %+v", recs)
	}
}

func TestSurvivalSweepEmptyStoreIsNoOp(t *testing.T) {
	dir := t.TempDir()
	cmd := survivalSweepCmd()
	cmd.SetArgs([]string{"--workdir", dir})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	var res survival.SweepResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse: %v (out=%q)", err, out)
	}
	if res.Scanned != 0 || res.Finalized != 0 {
		t.Errorf("expected no-op sweep on empty store, got %+v", res)
	}
}
