package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	write := func(dir, name, content, msg string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, dir, "add", "-A")
		gittest.Run(t, dir, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "-m", msg)
	}
	commit := func(dir, name, msg string) { write(dir, name, msg+"\n", msg) }
	// The #2669 shape: C1 and the base advance edit the same CHANGELOG a few
	// lines apart, so the rebase rewrites C1's hunk context and its patch-id.
	const changelog = "# Changelog\n\n## [Unreleased]\n\n- old a\n- old b\n"
	commit(work, "README.md", "base")
	write(work, "CHANGELOG.md", changelog, "changelog")
	gittest.Run(t, work, "push", "origin", "main")
	gittest.Run(t, work, "checkout", "-b", branch)
	write(work, "CHANGELOG.md", strings.Replace(changelog, "- old a\n", "- old a\n- C1 entry\n", 1), "C1")
	gittest.Run(t, work, "push", "origin", branch)
	remote = gittest.Run(t, work, "rev-parse", "HEAD")

	other := filepath.Join(root, "other")
	gittest.Run(t, root, "clone", origin, other)
	write(other, "CHANGELOG.md",
		strings.Replace(changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n- main entry\n", 1), "main advances")
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

// A rejected push that could not be reconciled (here a protected branch, which
// is never rewritten) must exit 3 with the marker. Exit 1 would let a stage defer
// past a branch that was never pushed.
func TestRunPushBranch_UnreconciledRejectionExits3(t *testing.T) {
	_, work, remote := pushBranchFixture(t, "epic/2699-z")
	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, true, &out, &errb); code != pushBranchForked {
		t.Fatalf("exit %d, want %d; stderr=%s", code, pushBranchForked, errb.String())
	}
	if !strings.Contains(errb.String(), "[branch-forked]") {
		t.Errorf("missing marker: %s", errb.String())
	}
	assertOutcome(t, out.Bytes(), "unreconciled")
	if got := gittest.Run(t, work, "ls-remote", "origin", "refs/heads/epic/2699-z"); !strings.HasPrefix(got, remote) {
		t.Errorf("origin moved: %s, want %s", got, remote)
	}
}

func assertOutcome(t *testing.T, data []byte, want string) {
	t.Helper()
	var res pushBranchResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("stdout = %q: %v", data, err)
	}
	if res.Outcome != want {
		t.Errorf("outcome = %q, want %q", res.Outcome, want)
	}
}

// The real unreconcilable shape on an ordinary branch: `merge-tree --write-tree`
// exits 129 on git older than 2.38, so the reconcile cannot run at all.
func TestRunPushBranch_OldGitMergeTreeExits3(t *testing.T) {
	_, work, remote := pushBranchFixture(t, "fix/2749-oldgit")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = merge-tree ] && exit 129\nexec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, true, &out, &errb); code != pushBranchForked {
		t.Fatalf("exit %d, want 3; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "[branch-forked] PUSH REJECTED: could not reconcile") {
		t.Errorf("stderr = %q, want the marker", errb.String())
	}
	assertOutcome(t, out.Bytes(), "unreconciled")
	if got := gittest.Run(t, work, "ls-remote", "origin", "refs/heads/fix/2749-oldgit"); !strings.HasPrefix(got, remote) {
		t.Errorf("origin moved: %s, want %s", got, remote)
	}
}

// Origin advances after a successful reconcile, so the recovery push is itself
// rejected. That exits 3 with the marker and leaves the branch as it was.
func TestRunPushBranch_RejectedRecoveryRetryExits3(t *testing.T) {
	const branch = "fix/2749-race"
	root, work, _ := pushBranchFixture(t, branch)
	local := gittest.Run(t, work, "rev-parse", "HEAD")
	racer := filepath.Join(root, "racer")
	gittest.Run(t, root, "clone", "-b", branch, filepath.Join(root, "origin.git"), racer)

	calls := 0
	pushBranchAttempt = func(ctx context.Context, dir, b string) (string, bool) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(filepath.Join(racer, "race.go"), []byte("race\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, racer, "add", "-A")
			gittest.Run(t, racer, "-c", "user.email=r@e", "-c", "user.name=R", "commit", "-m", "race")
			gittest.Run(t, racer, "push", "origin", branch)
		}
		return pushPlain(ctx, dir, b)
	}
	t.Cleanup(func() { pushBranchAttempt = pushPlain })

	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, true, &out, &errb); code != pushBranchForked {
		t.Fatalf("exit %d, want 3; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "[branch-forked] PUSH REJECTED: origin moved during recovery") {
		t.Errorf("stderr = %q, want the marker", errb.String())
	}
	assertOutcome(t, out.Bytes(), "forked")
	if got := gittest.Run(t, work, "rev-parse", "HEAD"); got != local {
		t.Errorf("HEAD = %s, want rollback to %s", got, local)
	}
}

// A verdict is not a usage error, so the verb must not print its usage.
func TestGitPushBranchCmd_VerdictPrintsNoUsage(t *testing.T) {
	_, work, _ := pushBranchFixture(t, "epic/2749-usage")
	t.Chdir(work)
	var out, errb bytes.Buffer
	cmd := gitPushBranchCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want a verdict error")
	}
	if strings.Contains(out.String()+errb.String(), "Usage:") {
		t.Errorf("usage printed on a verdict: %s%s", out.String(), errb.String())
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

func TestRunPushBranch_JSONRecoveryKeepsMarkerOnStderr(t *testing.T) {
	_, work, _ := pushBranchFixture(t, "fix/2699-json")
	var out, errb bytes.Buffer
	if code := runPushBranch(context.Background(), work, true, &out, &errb); code != pushBranchOK {
		t.Fatalf("exit %d; stderr=%s", code, errb.String())
	}
	var res pushBranchResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Outcome != "recovered" {
		t.Fatalf("stdout = %q (%v), want pure JSON with outcome recovered", out.String(), err)
	}
	if !strings.Contains(errb.String(), "[self-fork-recovered]") {
		t.Errorf("stderr = %q, want the [self-fork-recovered] marker", errb.String())
	}
}
