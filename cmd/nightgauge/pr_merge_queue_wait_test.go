package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/queuewait"
)

// queueMergeForge serves `pr merge` on a base branch with a merge queue: the
// PR is ready, the enqueue succeeds, and the first queue poll after it sees
// the PR merged, served queueHold after the enqueue.
type queueMergeForge struct {
	mu         sync.Mutex
	queueHold  time.Duration
	statusRead int
	enqueuedAt time.Time
}

func (f *queueMergeForge) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	q := string(body)
	w.Header().Set("Content-Type", "application/json")
	node := func(state string) map[string]any {
		n := map[string]any{
			"id": "PR_1", "number": 24, "state": state, "baseRefName": "main",
			"isMergeQueueEnabled": true, "isInMergeQueue": false,
			"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "headRefOid": "head1",
			"commits":         map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}},
			"mergeCommit":     nil,
			"mergeQueueEntry": nil,
		}
		if state == "MERGED" {
			n["mergeCommit"] = map[string]any{"oid": "landed1"}
		}
		return n
	}
	if strings.HasPrefix(r.URL.Path, "/repos/") { // squash defaults already PR_TITLE/PR_BODY (#2728)
		_ = json.NewEncoder(w).Encode(map[string]any{"squash_merge_commit_title": "PR_TITLE", "squash_merge_commit_message": "PR_BODY"})
		return
	}
	var data any
	switch {
	case strings.Contains(q, "prSquashSource"):
		data = map[string]any{"node": map[string]any{"number": 24, "title": "t",
			"baseRepository": map[string]any{"name": "widget", "owner": map[string]any{"login": "acme"}},
			"commits":        map[string]any{"nodes": []any{}}}}
	case strings.Contains(q, "enqueuePullRequest"):
		f.mu.Lock()
		f.enqueuedAt = time.Now()
		f.mu.Unlock()
		data = map[string]any{"enqueuePullRequest": map[string]any{"mergeQueueEntry": map[string]any{"state": "QUEUED", "position": 1}}}
	case strings.Contains(q, "isMergeQueueEnabled"):
		f.mu.Lock()
		f.statusRead++
		first := f.statusRead == 1
		f.mu.Unlock()
		if first {
			data = map[string]any{"node": node("OPEN")}
		} else {
			time.Sleep(f.queueHold) // the queue's CI run on the merge group
			data = map[string]any{"node": node("MERGED")}
		}
	default: // GetPR
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"id": "PR_1", "number": 24, "state": "OPEN", "headRefName": "feat/x", "labels": map[string]any{"nodes": []any{}}}}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// runPRMergeOverQueue runs `pr merge 24` in a fresh repository against f.
func runPRMergeOverQueue(t *testing.T, f *queueMergeForge, extra ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("NIGHTGAUGE_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NIGHTGAUGE_GITHUB_API_LOG", filepath.Join(t.TempDir(), "github-api.jsonl"))
	t.Cleanup(config.SwapMachineConfigPathForTest(func() (string, error) {
		return filepath.Join(home, ".nightgauge", "config.yaml"), nil
	}))
	root := layouttest.Repo(t)
	t.Chdir(root)

	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	prev := newClientFromConfigFn
	newClientFromConfigFn = func(gh.TokenResolver, string, string) (*gh.Client, error) {
		return gh.NewClientWithHTTPClient(&http.Client{Transport: rewriteTransport{srv}}), nil
	}
	t.Cleanup(func() { newClientFromConfigFn = prev })

	cmd := rootCmd()
	cmd.SetArgs(append([]string{"pr", "merge", "24", "--repo", "acme/widget", "--owner", "acme",
		"--delete-branch=false", "--json"}, extra...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pr merge: %v", err)
	}
	return root
}

// TestPRMerge_IssueRecordsTheQueueWaitOnTheBudgetedPath is #2770 AC 1, driven
// through prMergeCmd's own wiring: `pr merge --issue N` on a queue-required
// base writes one closed interval of this process spanning the enqueue to the
// return, and `pr-stage queue-wait` — what a budget enforcer reads — reports
// that wait, while time before the enqueue is not excluded.
func TestPRMerge_IssueRecordsTheQueueWaitOnTheBudgetedPath(t *testing.T) {
	f := &queueMergeForge{queueHold: 150 * time.Millisecond}
	spawn := time.Now()
	time.Sleep(50 * time.Millisecond) // review-fix time before the enqueue
	root := runPRMergeOverQueue(t, f, "--issue", "2770", "--force")
	returned := time.Now()

	rec, err := queuewait.Load(root, 2770)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Intervals) != 1 {
		t.Fatalf("intervals = %+v, want one", rec.Intervals)
	}
	iv := rec.Intervals[0]
	if iv.End == nil {
		t.Fatal("interval left open after pr merge returned")
	}
	if iv.PID != os.Getpid() {
		t.Errorf("interval writer pid = %d, want this process %d", iv.PID, os.Getpid())
	}
	if iv.Begin.Before(f.enqueuedAt) {
		t.Errorf("interval begins %s, before the enqueue at %s: pre-enqueue time would be excluded", iv.Begin, f.enqueuedAt)
	}
	if iv.End.After(returned) {
		t.Errorf("interval ends %s, after pr merge returned at %s", iv.End, returned)
	}
	if got := iv.End.Sub(iv.Begin); got < f.queueHold {
		t.Errorf("recorded queue wait %s, want at least the %s the queue held the PR", got, f.queueHold)
	}

	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"pr-stage", "queue-wait", "2770", "--since-ms", strconv.FormatInt(spawn.UnixMilli(), 10), "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("pr-stage queue-wait: %v", err)
	}
	var got prStageQueueWaitJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	excluded := time.Duration(got.ExcludedMs) * time.Millisecond
	elapsed := time.Since(spawn)
	if excluded < f.queueHold-time.Millisecond {
		t.Errorf("excluded %s, want at least the queue hold %s", excluded, f.queueHold)
	}
	if excluded > elapsed-50*time.Millisecond {
		t.Errorf("excluded %s of %s elapsed: the pre-enqueue time was excluded too", excluded, elapsed)
	}
}

// TestPRMerge_WithoutIssueRecordsNothing: a bare `pr merge` belongs to no
// budgeted stage and writes no record.
func TestPRMerge_WithoutIssueRecordsNothing(t *testing.T) {
	root := runPRMergeOverQueue(t, &queueMergeForge{})
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "queue-wait-*.json"))
	if len(matches) != 0 {
		t.Fatalf("pr merge without --issue wrote %v", matches)
	}
}
