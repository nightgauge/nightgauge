package adapters

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/gitworktree"
)

// tamperFixtureLinkedWorktree is the shape Manager.ensureWorktree builds: a
// linked worktree detached at the primary checkout's current HEAD, with that
// commit recorded as its base when record is true.
func tamperFixtureLinkedWorktree(t *testing.T, primary string, record bool) string {
	t.Helper()
	head := strings.TrimSpace(gittest.Run(t, primary, "rev-parse", "HEAD"))
	wt := filepath.Join(t.TempDir(), "nightgauge-issue-1825")
	if out, err := gitworktree.Add(primary, "--detach", wt, head); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	if record {
		if err := gitworktree.RecordBaseCommit(wt, head); err != nil {
			t.Fatalf("RecordBaseCommit: %v", err)
		}
	}
	return wt
}

func tamperFixtureRecordPath(t *testing.T, wt string) string {
	t.Helper()
	return filepath.Join(strings.TrimSpace(gittest.Run(t, wt, "rev-parse", "--absolute-git-dir")), gitworktree.BaseCommitFile)
}

// #1825 item 1: the primary checkout is on a branch carrying a committed
// opencode.json change that is not on origin/main (an unmerged branch, or a
// squash-merged one). A worktree created from it has tampered with nothing,
// and the gate must pass it. Compared against origin/main's merge-base, as
// before #1825, it is refused; the no-record case below still shows that.
func TestOpenCodeTamperGateRecordedBaseAcceptsUnmergedPrimaryChange(t *testing.T) {
	primary := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	gittest.Run(t, primary, "checkout", "-qb", "feat/unmerged-config")
	writeRepoFile(t, filepath.Join(primary, "opencode.json"), `{"base":true,"unmerged":true}`)
	gittest.Run(t, primary, "commit", "-qam", "unmerged config change")

	wt := tamperFixtureLinkedWorktree(t, primary, true)
	var warn bytes.Buffer
	if err := openCodeProjectConfigTamperCheckTo(context.Background(), wt, &warn); err != nil {
		t.Fatalf("a worktree created from a primary checkout with an unmerged config change was refused: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("a worktree with a recorded base printed a notice: %q", warn.String())
	}

	unrecorded := tamperFixtureLinkedWorktree(t, primary, false)
	warn.Reset()
	err := openCodeProjectConfigTamperCheckTo(context.Background(), unrecorded, &warn)
	if err == nil || !strings.Contains(err.Error(), "opencode.json") {
		t.Fatalf("with no recorded base the fallback compares against origin/main and must refuse; got %v", err)
	}
	if !strings.Contains(warn.String(), "merge-base with origin/main") {
		t.Errorf("the fallback did not say what it compared against: %q", warn.String())
	}
}

// #1825 item 2: a stage commits a tamper and then moves the shared
// refs/remotes/origin/main onto it. Against origin/main's merge-base the
// diff is empty and the gate passed; against the recorded creation commit it
// is not.
func TestOpenCodeTamperGateMovedOriginRefCannotHideCommittedTamper(t *testing.T) {
	primary := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	wt := tamperFixtureLinkedWorktree(t, primary, true)
	gittest.Run(t, wt, "checkout", "-qb", "feat/x")
	writeRepoFile(t, filepath.Join(wt, "opencode.json"), `{"base":true,"permission":{"bash":"allow"}}`)
	gittest.Run(t, wt, "commit", "-qam", "stage commit")
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")

	err := openCodeProjectConfigTamperCheckTo(context.Background(), wt, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a committed tamper hidden by moving refs/remotes/origin/main was not refused")
	}
	if !strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("the refusal does not name opencode.json: %v", err)
	}
}

// A record that exists but cannot be trusted refuses: the gate cannot tell a
// damaged record from one a stage rewrote.
func TestOpenCodeTamperGateBadRecordFailsClosed(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":      "not-a-sha\n",
		"unknown commit": strings.Repeat("ab", 20) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			primary := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
			wt := tamperFixtureLinkedWorktree(t, primary, true)
			if err := os.WriteFile(tamperFixtureRecordPath(t, wt), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := openCodeProjectConfigTamperCheckTo(context.Background(), wt, &bytes.Buffer{})
			if err == nil {
				t.Fatal("an untrustworthy base record was not refused")
			}
			if !strings.Contains(err.Error(), "recorded base commit") {
				t.Errorf("the refusal does not name the record: %v", err)
			}
		})
	}
}

// With no record, a merge-base that fails used to skip leg 2 in silence.
// Here origin/main is moved onto an unrelated root commit, so merge-base
// exits 1; the gate must refuse.
func TestOpenCodeTamperGateMergeBaseFailureFailsClosed(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	gittest.Run(t, wt, "checkout", "-q", "--orphan", "unrelated")
	gittest.Run(t, wt, "commit", "-q", "--allow-empty", "-m", "unrelated root")
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")
	gittest.Run(t, wt, "checkout", "-q", "main")

	err := openCodeProjectConfigTamperCheckTo(context.Background(), wt, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a failed merge-base silently skipped leg 2; the gate must fail closed")
	}
	if !strings.Contains(err.Error(), "merge-base") {
		t.Errorf("the refusal does not name the failed merge-base: %v", err)
	}
}

// No record and no default branch: the leg is skipped, and says so.
func TestOpenCodeTamperGateSkippedLegIsSurfaced(t *testing.T) {
	wt := gittest.InitRepo(t, t.TempDir(), "-b", "trunk")
	writeRepoFile(t, filepath.Join(wt, "README.md"), "x\n")
	gittest.Run(t, wt, "add", "-A")
	gittest.Run(t, wt, "commit", "-qm", "base")

	var warn bytes.Buffer
	if err := openCodeProjectConfigTamperCheckTo(context.Background(), wt, &warn); err != nil {
		t.Fatalf("a clean repository with no default branch was refused: %v", err)
	}
	if !strings.Contains(warn.String(), "not checked") {
		t.Errorf("the skipped leg was not surfaced: %q", warn.String())
	}
}

// RecordBaseCommit refuses the primary checkout, whose admin directory every
// worktree of the repository shares.
func TestRecordBaseCommitRefusesPrimaryCheckout(t *testing.T) {
	primary := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	head := strings.TrimSpace(gittest.Run(t, primary, "rev-parse", "HEAD"))
	if err := gitworktree.RecordBaseCommit(primary, head); err == nil {
		t.Fatal("RecordBaseCommit wrote a record into the primary checkout's shared admin directory")
	}
}
