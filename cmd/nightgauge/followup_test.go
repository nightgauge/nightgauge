package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/cmd/followup"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/github/githubtest"
)

// stubFollowupClient serves clientFromConfig from client, or fails it with err,
// so report mode's best-effort read never reaches a real forge.
func stubFollowupClient(t *testing.T, client *gh.Client, err error) {
	t.Helper()
	prev := newClientFromConfigFn
	newClientFromConfigFn = func(gh.TokenResolver, string, string) (*gh.Client, error) { return client, err }
	t.Cleanup(func() { newClientFromConfigFn = prev })
}

func runFollowup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := followupMaterializeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestFollowupMaterialize_RejectsBadIssueNumber(t *testing.T) {
	for _, a := range []string{"0", "-3", "abc"} {
		if _, err := runFollowup(t, a, "--run-dir", t.TempDir()); err == nil {
			t.Errorf("accepted %q", a)
		}
	}
}

// An empty capture is reported, not silent (#2638): a silent exit 0 cannot be
// told apart from a materializer that never ran.
func TestFollowupMaterialize_ZeroRecordsIsReported(t *testing.T) {
	out, err := runFollowup(t, "42", "--run-dir", t.TempDir(), "--workdir", t.TempDir())
	if err != nil || !strings.Contains(out, "#42: no discovered-work records") {
		t.Fatalf("err=%v out=%q", err, out)
	}
	out, err = runFollowup(t, "42", "--run-dir", t.TempDir(), "--workdir", t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("err=%v out=%q", err, out)
	}
	var res struct {
		Issue  int               `json:"issue"`
		Reason string            `json:"reason"`
		Items  []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v: %q", err, out)
	}
	if res.Issue != 42 || res.Reason != followup.EmptyCaptureReason || res.Items == nil {
		t.Fatalf("result = %+v", res)
	}
}

// With no config the gate fails closed to report-only. Building the GitHub
// client fails here, so the command warns that the parent's criteria are
// unavailable, prints the would-file list, and consumes the capture.
func TestFollowupMaterialize_ReportOnlyConsumesWithoutClient(t *testing.T) {
	stubFollowupClient(t, nil, errors.New("no token"))
	runDir := t.TempDir()
	rec := discoveredwork.Record{
		Summary: "Waiter ignores ceiling", Evidence: []string{"x"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := runFollowup(t, "42", "--run-dir", runDir, "--workdir", t.TempDir())
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "report-only: fail closed") || !strings.Contains(out, "would-file") ||
		!strings.Contains(out, "followup materialize: parent body unavailable: no token") {
		t.Fatalf("out = %s", out)
	}
	if _, err := os.Stat(filepath.Join(runDir, "discovered-work-42.json")); !os.IsNotExist(err) {
		t.Fatalf("capture not consumed: %v", err)
	}
}

func TestFollowupMaterialize_DryRunKeepsCapture(t *testing.T) {
	stubFollowupClient(t, nil, errors.New("no token"))
	runDir := t.TempDir()
	rec := discoveredwork.Record{
		Summary: "Something", Evidence: []string{"x"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := runFollowup(t, "42", "--run-dir", runDir, "--dry-run", "--workdir", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "discovered-work-42.json")); err != nil {
		t.Fatalf("dry-run consumed the capture: %v", err)
	}
}

// TestFollowupMaterialize_DryRunAppliesParentBody: --dry-run reads the
// parent's body, so a record restating one of its unchecked criteria is
// tracked-by-parent, not would-file, and nothing is written to the forge
// (#2872).
func TestFollowupMaterialize_DryRunAppliesParentBody(t *testing.T) {
	fake := githubtest.New(t, map[int]githubtest.Issue{
		42: {Body: "## Acceptance Criteria\n\n- [ ] The waiter honours the configured ceiling on every retry\n"},
	})
	stubFollowupClient(t, fake.Client(), nil)
	runDir := t.TempDir()
	for _, summary := range []string{
		"The waiter honours the configured ceiling on every retry",
		"Logger drops the run id from stage lines",
	} {
		rec := discoveredwork.Record{
			Summary: summary, Evidence: []string{"x"}, SourceStage: "feature-dev",
			IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
		}
		if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runFollowup(t, "42", "--run-dir", runDir, "--dry-run",
		"--repo", githubtest.Owner+"/"+githubtest.Repo, "--workdir", t.TempDir())
	if err != nil {
		t.Fatalf("err=%v out=%s", err, out)
	}
	for _, want := range []string{
		"tracked-by-parent",
		"The waiter honours the configured ceiling on every retry (restates an unchecked criterion of #42)",
		"would-file",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "would-file") != 1 {
		t.Fatalf("want exactly one would-file:\n%s", out)
	}
	if m := fake.Mutations(); len(m) != 0 {
		t.Fatalf("dry-run mutated the forge: %v", m)
	}
}

// In file mode a client that cannot be built is still an error: nothing may be
// filed blind.
func TestFollowupMaterialize_FileModeClientErrorIsFatal(t *testing.T) {
	stubFollowupClient(t, nil, errors.New("no token"))
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".nightgauge", "config.yaml"),
		[]byte("follow_up_filing:\n  mode: file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	rec := discoveredwork.Record{
		Summary: "Something", Evidence: []string{"x"}, SourceStage: "feature-dev",
		IssueNumber: 42, SuggestedType: "bug", SuggestedPriority: "medium", SuggestedSize: "S",
	}
	if _, _, err := discoveredwork.Append(runDir, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := runFollowup(t, "42", "--run-dir", runDir, "--workdir", workdir)
	if err == nil || !strings.Contains(err.Error(), "create GitHub client: no token") {
		t.Fatalf("err = %v", err)
	}
}
