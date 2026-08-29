package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// #1105. The verb is the discoverability floor: without it an operator has to
// already know refs/nightgauge/wip/ exists to find anything preserved in it.
// These drive the cobra command end to end against a real repository, because
// the failure being fixed was an absent reader, and a unit test of a formatter
// cannot tell whether the command looks at git at all.

type wipCLIRepo struct {
	t   *testing.T
	dir string
}

func newWipCLIRepo(t *testing.T) *wipCLIRepo {
	t.Helper()
	dir := t.TempDir()
	r := &wipCLIRepo{t: t, dir: dir}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@test")
	r.git("config", "user.name", "test")
	r.write("README", "base\n")
	r.git("add", ".")
	r.git("commit", "-m", "initial")
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
	return r
}

func (r *wipCLIRepo) git(args ...string) string {
	r.t.Helper()
	out, err := gittest.Command(r.dir, args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *wipCLIRepo) write(name, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

func (r *wipCLIRepo) preserve(branch string, issue int, stage string, ts int64, path, content string) string {
	r.t.Helper()
	r.git("checkout", "-q", "-b", branch, "main")
	r.write(path, content)
	r.git("add", "-A")
	r.git("commit", "-q", "-m", fmt.Sprintf(
		"wip(%s): preserve uncommitted work from a terminated stage\n\nRefs: #%d\nNightgauge-WIP: %s",
		stage, issue, stage))
	sha := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	ref := fmt.Sprintf("refs/nightgauge/wip/%s-%d", strings.ReplaceAll(branch, "/", "-"), ts)
	r.git("update-ref", ref, sha)
	r.git("checkout", "-q", "main")
	return ref
}

func (r *wipCLIRepo) refExists(ref string) bool {
	return gittest.Command(r.dir, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

func runWipCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := wipCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestWipList_NamesTheIssueBranchCommitAndPathCount(t *testing.T) {
	r := newWipCLIRepo(t)
	r.preserve("feat/338-guest-auth", 338, "feature-validate", 1787939337, "lib/auth.dart", "guest\n")

	out, err := runWipCmd(t, "list", "--workdir", r.dir)
	if err != nil {
		t.Fatalf("wip list: %v\n%s", err, out)
	}
	for _, want := range []string{"#338", "feat/338-guest-auth", "1 path(s)", "feature-validate", "refs/nightgauge/wip/"} {
		if !strings.Contains(out, want) {
			t.Errorf("wip list output is missing %q — an operator cannot act on what it does not name.\n%s", want, out)
		}
	}
}

func TestWipList_EmptyNamespaceSaysSoRatherThanFailing(t *testing.T) {
	r := newWipCLIRepo(t)
	out, err := runWipCmd(t, "list", "--workdir", r.dir)
	if err != nil {
		t.Fatalf("wip list on a clean repo: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No preserved work-in-progress refs") {
		t.Fatalf("expected an explicit empty listing, got:\n%s", out)
	}
}

func TestWipPrune_RemovesLandedWorkAndKeepsTheRest(t *testing.T) {
	r := newWipCLIRepo(t)
	landed := r.preserve("feat/338-landed", 338, "feature-validate", 1787939338, "lib/auth.dart", "guest\n")
	unlanded := r.preserve("feat/912-open", 912, "feature-dev", 1787939339, "lib/other.dart", "open\n")

	// Squash-shaped landing: the same content arrives on main as a new commit
	// that is not an ancestor of the preserved one.
	r.git("checkout", "-q", "main")
	r.write("lib/auth.dart", "guest\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "squash: land the work")
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))

	out, err := runWipCmd(t, "prune", "--workdir", r.dir)
	if err != nil {
		t.Fatalf("wip prune: %v\n%s", err, out)
	}
	if r.refExists(landed) {
		t.Errorf("the landed anchor survived prune, so the namespace still grows without bound.\n%s", out)
	}
	if !r.refExists(unlanded) {
		t.Errorf("prune deleted work that never landed — the exact loss #128 exists to prevent.\n%s", out)
	}
}
