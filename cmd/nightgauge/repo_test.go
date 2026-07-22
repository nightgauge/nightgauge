package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// fakeAutoMergeFetcher is the test double for autoMergeSettingsFetcher.
type fakeAutoMergeFetcher struct {
	allowAutoMerge bool
	repoFullName   string
	err            error

	calls []struct{ owner, repo string }
}

func (f *fakeAutoMergeFetcher) GetRepositorySettings(_ context.Context, owner, repo string) (*gh.RepositorySettings, error) {
	f.calls = append(f.calls, struct{ owner, repo string }{owner, repo})
	if f.err != nil {
		return nil, f.err
	}
	full := f.repoFullName
	if full == "" {
		full = owner + "/" + repo
	}
	return &gh.RepositorySettings{
		AllowAutoMerge: f.allowAutoMerge,
		RepoFullName:   full,
		Owner:          owner,
		Repo:           repo,
	}, nil
}

// withFakeFetcher swaps the package-level factory and restores it via t.Cleanup.
func withFakeFetcher(t *testing.T, fake *fakeAutoMergeFetcher) {
	t.Helper()
	orig := checkAutoMergeSettings
	checkAutoMergeSettings = func() (autoMergeSettingsFetcher, error) {
		return fake, nil
	}
	t.Cleanup(func() { checkAutoMergeSettings = orig })
}

func TestRepoCmd_HasCheckAutoMerge(t *testing.T) {
	cmd := repoCmd()
	if cmd.Use != "repo" {
		t.Errorf("Use = %q, want repo", cmd.Use)
	}
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"settings", "disable-auto-merge", "check-auto-merge", "enable-delete-branch"} {
		if !subs[want] {
			t.Errorf("missing %q subcommand under repo", want)
		}
	}
}

func TestRepoCheckAutoMergeCmd_Structure(t *testing.T) {
	cmd := repoCheckAutoMergeCmd()
	if cmd.Use != "check-auto-merge" {
		t.Errorf("Use = %q, want check-auto-merge", cmd.Use)
	}
	if !cmd.SilenceUsage {
		t.Error("SilenceUsage must be true so a by-design BLOCK exit doesn't print help")
	}
	for _, name := range []string{"owner", "repo", "json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	// MarkFlagRequired sets an annotation on the flag — verify it's present on --repo.
	if ann := cmd.Flags().Lookup("repo").Annotations["cobra_annotation_bash_completion_one_required_flag"]; len(ann) == 0 {
		t.Error("--repo must be marked required")
	}
}

func TestRepoCheckAutoMergeCmd_PassesWhenDisabled(t *testing.T) {
	withFakeFetcher(t, &fakeAutoMergeFetcher{allowAutoMerge: false, repoFullName: "nightgauge/myrepo"})

	cmd := repoCheckAutoMergeCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--owner", "nightgauge", "--repo", "myrepo"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("expected nil error on ALLOW, got: %v", err)
	}
}

func TestRepoCheckAutoMergeCmd_FailsWhenEnabled(t *testing.T) {
	withFakeFetcher(t, &fakeAutoMergeFetcher{allowAutoMerge: true, repoFullName: "nightgauge/myrepo"})

	cmd := repoCheckAutoMergeCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--owner", "nightgauge", "--repo", "myrepo"})

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected non-nil error when allow_auto_merge=true, got nil")
	}
	if !strings.Contains(err.Error(), "disable-auto-merge") {
		t.Errorf("error message must point at remediation verb, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "nightgauge/myrepo") {
		t.Errorf("error message must reference the offending repo, got: %q", err.Error())
	}
}

func TestRepoCheckAutoMergeCmd_JSON_Allow(t *testing.T) {
	withFakeFetcher(t, &fakeAutoMergeFetcher{allowAutoMerge: false, repoFullName: "nightgauge/myrepo"})

	// printJSON writes to os.Stdout, not cmd.OutOrStdout, so capture it directly.
	out := captureStdout(t, func() {
		cmd := repoCheckAutoMergeCmd()
		cmd.SetArgs([]string{"--owner", "nightgauge", "--repo", "myrepo", "--json"})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("expected nil error on ALLOW, got: %v", err)
		}
	})

	var got checkAutoMergeResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\noutput: %s", err, out)
	}
	if !got.Allowed {
		t.Error("allowed must be true when allow_auto_merge=false")
	}
	if got.AllowAutoMerge {
		t.Error("allow_auto_merge JSON field must be false")
	}
	if got.Repository != "nightgauge/myrepo" {
		t.Errorf("repository = %q, want nightgauge/myrepo", got.Repository)
	}
	if got.Reason == "" {
		t.Error("reason should not be empty even on ALLOW")
	}
}

func TestRepoCheckAutoMergeCmd_JSON_Block(t *testing.T) {
	withFakeFetcher(t, &fakeAutoMergeFetcher{allowAutoMerge: true, repoFullName: "nightgauge/myrepo"})

	var execErr error
	out := captureStdout(t, func() {
		cmd := repoCheckAutoMergeCmd()
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--owner", "nightgauge", "--repo", "myrepo", "--json"})
		execErr = cmd.ExecuteContext(context.Background())
	})

	if execErr == nil {
		t.Fatal("expected non-nil error when allow_auto_merge=true, even with --json")
	}

	var got checkAutoMergeResult
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\noutput: %s", jerr, out)
	}
	if got.Allowed {
		t.Error("allowed must be false when allow_auto_merge=true")
	}
	if !got.AllowAutoMerge {
		t.Error("allow_auto_merge JSON field must be true")
	}
}

func TestRepoCheckAutoMergeCmd_FetchError(t *testing.T) {
	withFakeFetcher(t, &fakeAutoMergeFetcher{err: errors.New("network broken")})

	cmd := repoCheckAutoMergeCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--owner", "nightgauge", "--repo", "myrepo"})

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when fetcher returns error, got nil")
	}
	if !strings.Contains(err.Error(), "check auto-merge") {
		t.Errorf("error must be wrapped with 'check auto-merge': got %q", err.Error())
	}
}
