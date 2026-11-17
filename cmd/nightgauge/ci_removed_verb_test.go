package main

import (
	"io"
	"strings"
	"testing"
)

// TestCICmd_RemovedParityCheckIsUnknown pins #2816: the removed parity-check
// verb must fail as an unknown command, not print help and exit 0.
func TestCICmd_RemovedParityCheckIsUnknown(t *testing.T) {
	root := rootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"ci", "parity-check"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("ci parity-check: want unknown-command error, got %v", err)
	}
	for _, sub := range ciCmd().Commands() {
		if sub.Name() == "parity-check" {
			t.Fatal("ci still registers parity-check")
		}
	}
}

// TestCICmd_BareGroupPrintsHelp keeps `nightgauge ci` with no verb a success.
func TestCICmd_BareGroupPrintsHelp(t *testing.T) {
	root := rootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"ci"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare ci: %v", err)
	}
}
