package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	want := []string{"ac-fingerprint <issue-number>", "ac-reconcile <issue-number>", "dependency-guard", "environment", "links", "managed-steering", "mitigation-rule", "platform-raw-http", "secrets", "skill-anti-patterns", "skill-bundle", "skill-includes", "skill-no-direct-gh", "skill-portability", "skill-shell-state", "syntax", "thinking-effort"}
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
	// skill-banners verbs, managed-steering (issue 1675) is the one deterministic
	// rewrite: it strips generated steering from the working-tree AGENTS.md.
	applicators := map[string]bool{"managed-steering": true}
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

// fakeACEmptyNotifier records --notify-empty's forge traffic (#2906).
type fakeACEmptyNotifier struct {
	existing []string
	posted   []string
}

func (f *fakeACEmptyNotifier) HasComment(_ context.Context, _ int, marker string) (bool, error) {
	for _, c := range append(append([]string{}, f.existing...), f.posted...) {
		if strings.Contains(c, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeACEmptyNotifier) AddComment(_ context.Context, _ int, body string) error {
	f.posted = append(f.posted, body)
	return nil
}

func runACReconcileNotify(t *testing.T, fake *fakeACEmptyNotifier, body string, flags ...string) {
	t.Helper()
	prev := newACEmptyNotifier
	newACEmptyNotifier = func(string, string) (acEmptyNotifier, error) { return fake, nil }
	t.Cleanup(func() { newACEmptyNotifier = prev })
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := preflightACReconcileCmd()
	cmd.SetArgs(append([]string{"7", "--body-file", bodyFile, "--workdir", dir,
		"--out", filepath.Join(dir, "out.json")}, flags...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ac-reconcile: %v", err)
	}
}

func TestPreflightACReconcile_NotifyEmpty(t *testing.T) {
	const noACs = "## Summary\n\nJust prose.\n"
	t.Run("posts once on no-acs-detected", func(t *testing.T) {
		fake := &fakeACEmptyNotifier{}
		runACReconcileNotify(t, fake, noACs, "--notify-empty")
		runACReconcileNotify(t, fake, noACs, "--notify-empty")
		if len(fake.posted) != 1 || !strings.HasPrefix(fake.posted[0], noACsMarker) ||
			!strings.Contains(fake.posted[0], "`Refs #7`") {
			t.Fatalf("posted = %q, want one marker comment naming Refs #7", fake.posted)
		}
	})
	t.Run("existing marker comment posts nothing", func(t *testing.T) {
		fake := &fakeACEmptyNotifier{existing: []string{"earlier\n" + noACsMarker}}
		runACReconcileNotify(t, fake, noACs, "--notify-empty")
		if len(fake.posted) != 0 {
			t.Fatalf("posted = %q, want none", fake.posted)
		}
	})
	t.Run("criteria found posts nothing", func(t *testing.T) {
		fake := &fakeACEmptyNotifier{}
		runACReconcileNotify(t, fake, "## Verification\n1. it works\n", "--notify-empty")
		if len(fake.posted) != 0 {
			t.Fatalf("posted = %q, want none", fake.posted)
		}
	})
	t.Run("flag absent posts nothing", func(t *testing.T) {
		fake := &fakeACEmptyNotifier{}
		runACReconcileNotify(t, fake, noACs)
		if len(fake.posted) != 0 {
			t.Fatalf("posted = %q, want none", fake.posted)
		}
	})
}
