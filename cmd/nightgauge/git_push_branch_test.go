package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// pushBranchFixture is a bare origin plus a working clone on `branch`, with
// the branch pushed once (C1) and then rebased onto an advanced main plus a
// new commit C2 — the #2669 shape. It returns the clone and origin's tip.
func pushBranchFixture(t *testing.T, branch string) (root, work, remote string) {
	t.Helper()
	root = t.TempDir()
	origin := filepath.Join(root, "origin.git")
	gittest.Run(t, root, "init", "--bare", "-b", "main", origin)
	work = filepath.Join(root, "work")
	gittest.Run(t, root, "clone", origin, work)
	commit := func(dir, name, msg string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, dir, "add", "-A")
		gittest.Run(t, dir, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-m", msg)
	}
	commit(work, "README.md", "base")
	gittest.Run(t, work, "push", "origin", "main")
	gittest.Run(t, work, "checkout", "-b", branch)
	commit(work, "impl.go", "C1")
	gittest.Run(t, work, "push", "origin", branch)
	remote = gittest.Run(t, work, "rev-parse", "HEAD")

	other := filepath.Join(root, "other")
	gittest.Run(t, root, "clone", origin, other)
	commit(other, "main.go", "main advances")
	gittest.Run(t, other, "push", "origin", "main")

	gittest.Run(t, work, "fetch", "origin", "main")
	gittest.Run(t, work, "-c", "user.email=t@e", "-c", "user.name=T", "rebase", "origin/main")
	commit(work, "impl2.go", "C2")
	return root, work, remote
}

func TestRunPushBranch_RecoversSelfForkWithoutForce(t *testing.T) {
	_, work, remote := pushBranchFixture(t, "fix/2669-x")
	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, false, &out, &errb); code != pushBranchOK {
		t.Fatalf("exit %d, want 0; stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "[self-fork-recovered] origin/fix/2669-x "+remote[:8]+"..") {
		t.Errorf("stdout = %q, want the [self-fork-recovered] line", out.String())
	}
	head := strings.Fields(gittest.Run(t, work, "ls-remote", "origin", "refs/heads/fix/2669-x"))[0]
	if head != gittest.Run(t, work, "rev-parse", "HEAD") {
		t.Errorf("origin head %s != local HEAD", head)
	}
	gittest.Run(t, work, "merge-base", "--is-ancestor", remote, head)
}

func TestRunPushBranch_GenuineForkExits3(t *testing.T) {
	root, work, _ := pushBranchFixture(t, "fix/2699-y")
	op := filepath.Join(root, "op")
	gittest.Run(t, root, "clone", "-b", "fix/2699-y", filepath.Join(root, "origin.git"), op)
	if err := os.WriteFile(filepath.Join(op, "op.go"), []byte("op\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, op, "add", "-A")
	gittest.Run(t, op, "-c", "user.email=o@e", "-c", "user.name=O", "commit", "-m", "operator")
	gittest.Run(t, op, "push", "origin", "fix/2699-y")
	opSHA := gittest.Run(t, op, "rev-parse", "HEAD")

	var out, errb bytes.Buffer
	code := runPushBranch(context.Background(), work, true, &out, &errb)
	if code != pushBranchForked {
		t.Fatalf("exit %d, want 3; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "[branch-forked] PUSH REJECTED") {
		t.Errorf("stderr = %q, want the [branch-forked] marker", errb.String())
	}
	var res pushBranchResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "forked" || len(res.Unmatched) != 1 || res.Unmatched[0] != opSHA {
		t.Errorf("json = %+v, want forked naming %s", res, opSHA)
	}
	if got := strings.Fields(gittest.Run(t, work, "ls-remote", "origin", "refs/heads/fix/2699-y"))[0]; got != opSHA {
		t.Errorf("origin moved on a genuine fork: %s", got)
	}
}

func TestRunPushBranch_PlainPushAndFailure(t *testing.T) {
	_, work, _ := pushBranchFixture(t, "fix/2699-z")
	gittest.Run(t, work, "checkout", "-b", "fresh")
	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, false, &out, &errb); code != pushBranchOK {
		t.Fatalf("new branch push exit %d; stderr=%s", code, errb.String())
	}
	gittest.Run(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if code := runPushBranch(context.Background(), work, false, &out, &errb); code != pushBranchFailed {
		t.Errorf("unreachable origin exit %d, want 1", code)
	}
}
