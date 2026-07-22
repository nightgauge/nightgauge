package main

import (
	"sort"
	"testing"

	"github.com/spf13/cobra"
)

func TestPreflightCmd_RegistersAllSubcommands(t *testing.T) {
	cmd := preflightCmd()
	if cmd.Use != "preflight" {
		t.Errorf("Use = %q, want %q", cmd.Use, "preflight")
	}
	subs := cmd.Commands()
	got := make([]string, 0, len(subs))
	for _, s := range subs {
		got = append(got, s.Use)
	}
	sort.Strings(got)
	want := []string{"ac-reconcile <issue-number>", "dependency-guard", "links", "secrets", "skill-anti-patterns", "skill-no-direct-gh", "skill-portability", "syntax"}
	if len(got) != len(want) {
		t.Fatalf("subcommand count = %d, want %d (got: %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("subcommand[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPreflightCmd_FixFlagOnlyOnApplicators(t *testing.T) {
	// --fix is the apply-mode flag; it belongs only on subcommands that
	// implement a deterministic in-place rewrite. After the skills-canonical
	// migration (#3876) retired the command-wrapper-coupled skill-versions and
	// skill-banners verbs, no remaining subcommand has a deterministic auto-fix.
	applicators := map[string]bool{}
	cmd := preflightCmd()
	for _, sub := range cmd.Commands() {
		fix := sub.Flags().Lookup("fix")
		if applicators[sub.Use] {
			if fix == nil {
				t.Errorf("--fix flag missing from %s", sub.Use)
			}
		} else {
			if fix != nil {
				t.Errorf("--fix flag present on %s; expected only on applicators (%v)", sub.Use, applicators)
			}
		}
	}
}

func TestPreflightCmd_AllSubcommandsHaveJSONFlag(t *testing.T) {
	cmd := preflightCmd()
	for _, sub := range cmd.Commands() {
		if f := sub.Flags().Lookup("json"); f == nil {
			t.Errorf("--json flag missing from %s", sub.Use)
		}
	}
}

func TestPreflightCmd_LinksFlagsMatchDocsCheckLinks(t *testing.T) {
	cmd := preflightCmd()
	var links *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "links" {
			links = sub
			break
		}
	}
	if links == nil {
		t.Fatal("links subcommand not found")
	}
	for _, name := range []string{"root", "target", "section", "exclude-templates", "json"} {
		if f := links.Flags().Lookup(name); f == nil {
			t.Errorf("links subcommand missing --%s", name)
		}
	}
}

func TestPreflightCmd_SyntaxAndSecretsHaveWorkdirFlag(t *testing.T) {
	cmd := preflightCmd()
	for _, sub := range cmd.Commands() {
		if sub.Use == "syntax" || sub.Use == "secrets" {
			if f := sub.Flags().Lookup("workdir"); f == nil {
				t.Errorf("%s subcommand missing --workdir", sub.Use)
			}
		}
	}
}
