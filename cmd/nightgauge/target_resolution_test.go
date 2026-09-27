package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
)

// TestNoVendorOwnerRepoDefault is the #2198 fence: no command may default
// --owner or --repo to the vendor's own org/repo. Before config.yaml exists
// (onboarding) such a default silently targeted nightgauge/nightgauge.
func TestNoVendorOwnerRepoDefault(t *testing.T) {
	var walk func(c *cobra.Command)
	count := 0
	walk = func(c *cobra.Command) {
		for _, name := range []string{"owner", "repo"} {
			for _, f := range []*cobra.Command{c} {
				if fl := f.Flags().Lookup(name); fl != nil {
					count++
					if strings.Contains(fl.DefValue, "nightgauge") {
						t.Errorf("%s: --%s DefValue = %q, want no vendor default", c.CommandPath(), name, fl.DefValue)
					}
				}
				if fl := f.PersistentFlags().Lookup(name); fl != nil && strings.Contains(fl.DefValue, "nightgauge") {
					t.Errorf("%s: persistent --%s DefValue = %q", c.CommandPath(), name, fl.DefValue)
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd())
	if count == 0 {
		t.Fatal("walked no owner/repo flags; the walk is broken")
	}
}

func labelEnsureForTest(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := findSubcommand(t, rootCmd(), "label", "ensure")
	if cmd.Annotations[ownerRequiredAnnotation] != "true" || cmd.Annotations[repoRequiredAnnotation] != "true" {
		t.Fatalf("label ensure must register --owner/--repo via ownerFlag/requiredRepoNameFlag")
	}
	return cmd
}

func stubOrigin(t *testing.T, owner, name string, ok bool) {
	t.Helper()
	prev := originSlugFn
	originSlugFn = func(string) (string, string, bool) { return owner, name, ok }
	t.Cleanup(func() { originSlugFn = prev })
}

func TestResolveTargetFlags_GitOriginFallback(t *testing.T) {
	stubOrigin(t, "acme", "widgets", true)
	cmd := labelEnsureForTest(t)
	if err := resolveTargetFlags(cmd, nil, "/somewhere"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if o, _ := flagValue(cmd, "owner"); o != "acme" {
		t.Errorf("owner = %q, want acme", o)
	}
	if r, _ := flagValue(cmd, "repo"); r != "widgets" {
		t.Errorf("repo = %q, want widgets", r)
	}
}

func TestResolveTargetFlags_Precedence(t *testing.T) {
	stubOrigin(t, "acme", "widgets", true)

	// config beats origin
	cmd := labelEnsureForTest(t)
	if err := resolveTargetFlags(cmd, &config.Config{Owner: "cfgorg", DefaultRepo: "cfgrepo"}, "/x"); err != nil {
		t.Fatal(err)
	}
	if o, _ := flagValue(cmd, "owner"); o != "cfgorg" {
		t.Errorf("owner = %q, want cfgorg", o)
	}
	if r, _ := flagValue(cmd, "repo"); r != "cfgrepo" {
		t.Errorf("repo = %q, want cfgrepo", r)
	}

	// explicit flag beats config
	cmd = labelEnsureForTest(t)
	_ = cmd.Flags().Set("repo", "flagrepo")
	if err := resolveTargetFlags(cmd, &config.Config{Owner: "cfgorg", DefaultRepo: "cfgrepo"}, "/x"); err != nil {
		t.Fatal(err)
	}
	if r, _ := flagValue(cmd, "repo"); r != "flagrepo" {
		t.Errorf("repo = %q, want flagrepo", r)
	}

	// an explicit owner that differs from origin must not borrow origin's name
	cmd = labelEnsureForTest(t)
	_ = cmd.Flags().Set("owner", "someoneelse")
	err := resolveTargetFlags(cmd, nil, "/x")
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Errorf("mismatched owner must leave --repo unresolved, got err=%v", err)
	}
}

func TestResolveTargetFlags_ErrorWhenUnresolvable(t *testing.T) {
	stubOrigin(t, "", "", false)
	cmd := labelEnsureForTest(t)
	err := resolveTargetFlags(cmd, nil, "/x")
	if err == nil {
		t.Fatal("want error with no flag, config, or origin")
	}
	for _, want := range []string{"--owner", "--repo", "config.yaml", "origin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

// End to end through cobra: a fresh git repo with no origin and no config
// must fail before any API call.
func TestLabelEnsure_FreshRepoFailsClosed(t *testing.T) {
	stubOrigin(t, "", "", false)
	dir := t.TempDir()
	t.Chdir(dir)
	root := rootCmd()
	root.SetArgs([]string{"label", "ensure", "--json"})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("want missing --repo error, got %v", err)
	}
}
