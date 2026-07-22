package main

import "testing"

func TestIntelligenceCmd_Structure(t *testing.T) {
	cmd := intelligenceCmd()
	if cmd.Use != "intelligence" {
		t.Errorf("Use = %q, want intelligence", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	if !subs["loop-verdicts"] {
		t.Error("missing 'loop-verdicts' subcommand")
	}
}

func TestIntelligenceLoopVerdictsCmd_Flags(t *testing.T) {
	cmd := intelligenceLoopVerdictsCmd()
	if cmd.Use != "loop-verdicts" {
		t.Errorf("Use = %q, want loop-verdicts", cmd.Use)
	}
	if f := cmd.Flags().Lookup("workdir"); f == nil {
		t.Error("missing --workdir flag")
	}
	if f := cmd.Flags().Lookup("period"); f == nil {
		t.Error("missing --period flag")
	}
	// period should default to 30
	period, err := cmd.Flags().GetInt("period")
	if err != nil {
		t.Fatalf("get period flag: %v", err)
	}
	if period != 30 {
		t.Errorf("period default = %d, want 30", period)
	}
}

func TestIntelligenceCmd_RegisteredInRoot(t *testing.T) {
	root := rootCmd()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "intelligence" {
			found = true
			break
		}
	}
	if !found {
		t.Error("intelligence command not registered in root")
	}
}
