package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/dockercompose"
)

// installFakeDockerForCleanup writes a recording shim onto PATH and returns
// the call-log path. Mirrors the fixture in internal/dockercompose but
// embedded here so the cmd test stays self-contained.
func installFakeDockerForCleanup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "$FAKE_DOCKER_LOG"
case "$1" in
  version) exit "${FAKE_DOCKER_VERSION_EXIT:-0}" ;;
  compose)
    case "$2" in
      ls) printf '%s' "${FAKE_DOCKER_LS_OUTPUT:-[]}" ; exit 0 ;;
      -p) [ "$4" = "down" ] && exit 0 ;;
    esac ;;
  images) printf '%s' "${FAKE_DOCKER_IMAGES_OUT:-}" ; exit 0 ;;
  rmi) exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	return logPath
}

func TestSelectCleanupTargets_OrphanedOnly(t *testing.T) {
	projects := []dockercompose.Project{
		{Name: "issue-42", IssueNumber: 42},
		{Name: "issue-7", IssueNumber: 7},
		{Name: "issue-99", IssueNumber: 99},
	}
	active := map[int]bool{42: true} // only issue-42 has a live worktree

	got := selectCleanupTargets(projects, active, true, false)
	if len(got) != 2 {
		t.Fatalf("expected 2 orphaned targets, got %d", len(got))
	}
	names := []string{got[0].Name, got[1].Name}
	if !contains(names, "issue-7") || !contains(names, "issue-99") {
		t.Errorf("expected issue-7 and issue-99 to be selected, got %v", names)
	}
	if contains(names, "issue-42") {
		t.Errorf("issue-42 has an active worktree — must NOT be selected as orphan")
	}
}

func TestSelectCleanupTargets_AllFlagOverridesOrphaned(t *testing.T) {
	projects := []dockercompose.Project{
		{Name: "issue-42", IssueNumber: 42},
		{Name: "issue-7", IssueNumber: 7},
	}
	active := map[int]bool{42: true}

	got := selectCleanupTargets(projects, active, true /* orphanedOnly */, true /* all */)
	if len(got) != 2 {
		t.Errorf("--all must include every project, got %d", len(got))
	}
}

func TestExtractIssueNumber(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"issue-42", 42, true},
		{"nightgauge-issue-836", 836, true},
		{"issue-", 0, false},
		{"issue-abc", 0, false},
		{"random-dir", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		gotN, gotOK := extractIssueNumber(tc.in)
		if gotN != tc.want || gotOK != tc.wantOK {
			t.Errorf("extractIssueNumber(%q) = (%d,%v), want (%d,%v)", tc.in, gotN, gotOK, tc.want, tc.wantOK)
		}
	}
}

func TestCleanupCmd_DryRunDoesNotInvokeDown(t *testing.T) {
	logPath := installFakeDockerForCleanup(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT", `[{"Name":"issue-42","Status":"running(1)"}]`)

	cmd := cleanupCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--dry-run", "--all", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cleanup --dry-run --all: %v", err)
		}
	})

	calls := readFile(t, logPath)
	if strings.Contains(calls, "down -v") {
		t.Errorf("--dry-run must not run docker compose down, got log:\n%s", calls)
	}
	if !strings.Contains(stdout, "\"dry_run\": true") {
		t.Errorf("expected dry_run=true in JSON output, got:\n%s", stdout)
	}
}

func TestCleanupCmd_AllTearsDownEveryProject(t *testing.T) {
	logPath := installFakeDockerForCleanup(t)
	t.Setenv("FAKE_DOCKER_LS_OUTPUT",
		`[{"Name":"issue-42","Status":"running(1)"},{"Name":"issue-7","Status":"exited(0)"}]`)

	cmd := cleanupCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--all", "--json"})

	_ = captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cleanup --all: %v", err)
		}
	})

	calls := readFile(t, logPath)
	if !strings.Contains(calls, "compose -p issue-42 down -v --remove-orphans") {
		t.Errorf("expected teardown for issue-42, got log:\n%s", calls)
	}
	if !strings.Contains(calls, "compose -p issue-7 down -v --remove-orphans") {
		t.Errorf("expected teardown for issue-7, got log:\n%s", calls)
	}
}

func TestCleanupCmd_ReportsDockerUnavailableGracefully(t *testing.T) {
	installFakeDockerForCleanup(t)
	t.Setenv("FAKE_DOCKER_VERSION_EXIT", "1")

	cmd := cleanupCmd()
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--json"})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cleanup must succeed when docker missing, got: %v", err)
		}
	})
	if !strings.Contains(stdout, "\"available\": false") {
		t.Errorf("expected available=false in output, got:\n%s", stdout)
	}
}

// helpers ---------------------------------------------------------------

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// captureStdout runs fn while routing os.Stdout through a pipe; returns
// everything written. Cobra's printJSON helper prints via fmt.Println which
// targets os.Stdout — not the cobra command's bound writer — so we have to
// intercept at the OS level rather than via cmd.SetOut.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	doneCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		doneCh <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = origStdout
	return <-doneCh
}
