package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

func refreshGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gittest.Command(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func refreshCommit(t *testing.T, dir, path, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshGit(t, dir, "add", ".")
	refreshGit(t, dir, "commit", "-q", "-m", "change "+path)
}

// refreshRepos returns an upstream on main and a clone on issue 42's branch.
func refreshRepos(t *testing.T) (upstream, clone string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
	upstream = t.TempDir()
	refreshGit(t, upstream, "init", "-q", "-b", "main")
	refreshCommit(t, upstream, "a.txt", "a\n")
	clone = filepath.Join(t.TempDir(), "clone")
	refreshGit(t, filepath.Dir(clone), "clone", "-q", upstream, clone)
	refreshGit(t, clone, "checkout", "-q", "-b", "feat/42-x")
	return upstream, clone
}

func runRefreshBase(t *testing.T, clone string) prStageRefreshBaseJSON {
	t.Helper()
	cmd := prStageRefreshBaseCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"42", "--workdir", clone})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("refresh-base: %v", err)
	}
	var out prStageRefreshBaseJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("parse %q: %v", buf.String(), err)
	}
	for _, key := range []string{`"skipped"`, `"stop_reason"`, `"conflict_files"`, `"outcome"`, `"behind"`} {
		if !strings.Contains(buf.String(), key) {
			t.Fatalf("output %q lacks %s", buf.String(), key)
		}
	}
	return out
}

func TestPrStageRefreshBase_UpToDate(t *testing.T) {
	_, clone := refreshRepos(t)
	out := runRefreshBase(t, clone)
	if out.Outcome != gitpkg.BaseMergeUpToDate || out.Skipped != "" || out.StopReason != "" {
		t.Fatalf("got %+v", out)
	}
}

func TestPrStageRefreshBase_FastForwardsWithoutMergeCommit(t *testing.T) {
	upstream, clone := refreshRepos(t)
	refreshCommit(t, upstream, "b.txt", "b\n")
	out := runRefreshBase(t, clone)
	if out.Outcome != gitpkg.BaseMergeMerged || out.Behind != 1 || out.Ahead != 0 || out.StopReason != "" {
		t.Fatalf("got %+v", out)
	}
	if head, remote := refreshGit(t, clone, "rev-parse", "HEAD"), refreshGit(t, clone, "rev-parse", "origin/main"); head != remote {
		t.Fatalf("HEAD %s != origin/main %s: not a fast-forward", head, remote)
	}
}

func TestPrStageRefreshBase_ConflictStopsAndLeavesTree(t *testing.T) {
	upstream, clone := refreshRepos(t)
	refreshCommit(t, upstream, "a.txt", "upstream\n")
	refreshCommit(t, clone, "a.txt", "mine\n")
	before := refreshGit(t, clone, "rev-parse", "HEAD")
	out := runRefreshBase(t, clone)
	if out.Outcome != gitpkg.BaseMergeConflict || !strings.HasPrefix(out.StopReason, "[merge-conflict]") {
		t.Fatalf("got %+v", out)
	}
	if len(out.ConflictFiles) != 1 || out.ConflictFiles[0] != "a.txt" {
		t.Fatalf("conflict_files = %v", out.ConflictFiles)
	}
	if after := refreshGit(t, clone, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved %s -> %s", before, after)
	}
	if st := refreshGit(t, clone, "status", "--porcelain"); st != "" {
		t.Fatalf("tree not clean after abort: %s", st)
	}
}

func TestPrStageRefreshBase_DetachedHeadIsSkipped(t *testing.T) {
	_, clone := refreshRepos(t)
	refreshGit(t, clone, "checkout", "-q", "--detach")
	out := runRefreshBase(t, clone)
	if out.Skipped == "" || out.StopReason != "" {
		t.Fatalf("got %+v", out)
	}
}

func TestPrStageRefreshBase_BadIssueIsAUsageError(t *testing.T) {
	cmd := prStageRefreshBaseCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"nope", "--workdir", t.TempDir()})
	if err := cmd.Execute(); err == nil || buf.Len() != 0 {
		t.Fatalf("err=%v out=%q", err, buf.String())
	}
}
