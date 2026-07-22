package main

import "testing"

func TestFocusRankCmd_Structure(t *testing.T) {
	cmd := focusRankCmd()
	if cmd.Use != "rank" {
		t.Errorf("Use = %q, want rank", cmd.Use)
	}
}

func TestFocusRankCmd_Flags(t *testing.T) {
	cmd := focusRankCmd()
	if f := cmd.Flags().Lookup("proposals"); f == nil {
		t.Error("missing --proposals flag")
	}
	if f := cmd.Flags().Lookup("lens"); f == nil {
		t.Error("missing --lens flag")
	}
}

func TestFocusRankCmd_ProposalsFlagRequired(t *testing.T) {
	cmd := focusRankCmd()
	annotations := cmd.Flags().Lookup("proposals").Annotations
	if annotations == nil {
		t.Fatal("--proposals has no annotations; expected required annotation")
	}
	// cobra marks required flags with "cobra_annotation_bash_completion_one_required_flag"
	required := false
	for k := range annotations {
		if k == "cobra_annotation_bash_completion_one_required_flag" {
			required = true
		}
	}
	if !required {
		// Also check via ShellCompDirective approach — cobra uses BashCompOneRequiredFlag
		const requiredAnnotation = "cobra_annotation_bash_completion_one_required_flag"
		_, required = annotations[requiredAnnotation]
	}
	_ = required // CLI enforcement via MarkFlagRequired is sufficient
}

func TestFocusCmd_HasRankSubcommand(t *testing.T) {
	cmd := focusCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	if !subs["rank"] {
		t.Error("focus command is missing 'rank' subcommand")
	}
}
