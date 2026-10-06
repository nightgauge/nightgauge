package main

import (
	"strings"
	"testing"
)

// `nightgauge run --private` keeps one run the member starts private (#2400);
// runs the autonomous loop starts are team-visible, so --auto refuses it
// before anything is configured or started.
func TestRunPrivateFlag(t *testing.T) {
	cmd := runCmd()
	if f := cmd.Flags().Lookup("private"); f == nil || f.DefValue != "false" {
		t.Fatalf("run --private flag = %+v, want a boolean defaulting to false", f)
	}

	cmd.SetArgs([]string{"--auto", "--private"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--private") {
		t.Fatalf("run --auto --private: err = %v, want a refusal naming --private", err)
	}
}
