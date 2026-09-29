package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/flock"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// #2093 wiring: the hygiene remedies #2089 declares are executable end to end
// through the engine, against real repositories, and each verb's precondition
// refuses when the scan's claim no longer holds.

// registryOf is the default registry narrowed to ids, in the given order.
func registryOf(t *testing.T, ids ...string) *Registry {
	t.Helper()
	all := map[string]Check{}
	for _, c := range DefaultRegistry().Checks() {
		c.DependsOn = nil
		all[c.ID] = c
	}
	reg := NewRegistry()
	for _, id := range ids {
		c, ok := all[id]
		if !ok {
			t.Fatalf("no registered check %q", id)
		}
		reg.MustRegister(c)
	}
	return reg
}

func hygieneFixer(t *testing.T, dir string, deps verbDeps, ids ...string) *Fixer {
	t.Helper()
	env := &Env{Cwd: dir, Now: aged()}
	return &Fixer{
		Registry: registryOf(t, ids...), Verbs: builtinVerbs(env, deps), Env: env,
		Log: &FixLog{Path: filepath.Join(t.TempDir(), "fix-log.jsonl")},
	}
}

func resultFor(t *testing.T, rep FixReport, code, key, value string) FixResult {
	t.Helper()
	for _, r := range rep.Results {
		if r.Finding.Code == code && r.Finding.Evidence[key] == value {
			return r
		}
	}
	t.Fatalf("no %s result with %s=%s among %d result(s)", code, key, value, len(rep.Results))
	return FixResult{}
}

// mergedWorktree plants a pipeline worktree whose branch is squash-merged.
func mergedWorktree(r *leakRepo, issue int) string {
	path := filepath.Join(r.dir, ".worktrees", "issue-"+itoa(issue))
	r.git("worktree", "add", "-q", path, "-b", "fix/"+itoa(issue), "main")
	name := fmt.Sprintf("landed-%d.txt", issue)
	if err := os.WriteFile(filepath.Join(path, name), []byte("shipped\n"), 0o644); err != nil {
		r.t.Fatalf("write: %v", err)
	}
	r.git("-C", path, "add", ".")
	r.git("-C", path, "commit", "-m", "work")
	r.write(name, "shipped\n")
	r.git("add", ".")
	r.git("commit", "-m", "squash: fix/"+itoa(issue))
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
	return path
}

func TestHygieneRemedies_WorktreeSweepRemovesOnlyTheSelectedWorktree(t *testing.T) {
	r := newLeakRepo(t)
	a := mergedWorktree(r, 1201)
	b := mergedWorktree(r, 1202)
	fx := hygieneFixer(t, r.dir, verbDeps{}, "worktree_leaks")

	fx.Scan(context.Background())
	var fa Finding
	for _, f := range fx.results[0].Findings {
		if f.Evidence["path"] == a {
			fa = f
		}
	}
	res := fx.ApplyFingerprint(context.Background(), "worktree_leaks", fa.Fingerprint, "sweep", false)
	if res.Outcome != OutcomeFixed {
		t.Fatalf("outcome %s (%s), want fixed", res.Outcome, res.Detail)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("the selected worktree %s still exists", a)
	}
	if _, err := os.Stat(b); err != nil {
		t.Errorf("the unselected worktree %s was removed: %v", b, err)
	}
}

func TestHygieneRemedies_WorktreeSweepRefusesALiveRun(t *testing.T) {
	r := newLeakRepo(t)
	path := mergedWorktree(r, 1203)
	fx := hygieneFixer(t, r.dir, verbDeps{}, "worktree_leaks")
	f := fx.Scan(context.Background())[0].Findings[0]
	// A run starts on the issue after the scan: its non-terminal snapshot is
	// the in-flight evidence the sweep honours.
	writeRunSnapshot(t, r.dir, 1203)
	res := fx.ApplyFingerprint(context.Background(), "worktree_leaks", f.Fingerprint, "sweep", false)
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome %s (%s), want blocked for a live run's worktree", res.Outcome, res.Detail)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a live run's worktree was removed: %v", err)
	}
}

// TestHygieneRemedies_FixtureEndToEnd drives the #2089 fixture through one
// pass: the stash is restored, the merged branch deleted with --yes, the
// unmerged branch and the dirty worktree left as manual.
func TestHygieneRemedies_FixtureEndToEnd(t *testing.T) {
	r := hygieneFixture(t)
	// The fixture nests worktrees inside the checkout (production keeps them
	// outside it); ignore them so the main tree reads clean, as it would.
	r.write(".git/info/exclude", ".worktrees/\n")
	fx := hygieneFixer(t, r.dir, verbDeps{}, "worktree_leaks", "stranded_branches", "pipeline_stashes")

	dry := fx.Run(context.Background(), FixOptions{DryRun: true, Yes: true})
	if dry.Counts.Previewed != 2 || dry.Counts.Manual != 2 {
		t.Fatalf("dry run counts %+v, want 2 previewed and 2 manual", dry.Counts)
	}
	if !strings.Contains(r.git("branch", "--list", "fix/912-landed"), "fix/912-landed") {
		t.Fatal("--dry-run deleted the branch")
	}

	rep := fx.Run(context.Background(), FixOptions{})
	if got := resultFor(t, rep, "NGD018", "branch", "fix/912-landed"); got.Action != ActionAwaitingConsent {
		t.Errorf("merged branch without --yes: %s", got.Action)
	}
	if got := resultFor(t, rep, "NGD019", "stash_ref", "stash@{0}"); got.Outcome != OutcomeFixed {
		t.Errorf("stash sweep: %s (%s)", got.Outcome, got.Detail)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, "README")); string(b) != "modified\n" {
		t.Errorf("the stash was not restored: README = %q", b)
	}

	rep = fx.Run(context.Background(), FixOptions{Yes: true, Only: []string{"stranded_branches"}})
	if got := resultFor(t, rep, "NGD018", "branch", "fix/912-landed"); got.Outcome != OutcomeFixed {
		t.Fatalf("branch delete: %s (%s)", got.Outcome, got.Detail)
	}
	if out := r.git("branch", "--list", "fix/912-landed"); strings.TrimSpace(out) != "" {
		t.Errorf("the merged branch still exists: %q", out)
	}
	if got := resultFor(t, rep, "NGD018", "branch", "feat/919-unlanded"); got.Action != ActionManual {
		t.Errorf("unmerged branch: %s, want manual", got.Action)
	}
	if !strings.Contains(r.git("branch", "--list", "feat/919-unlanded"), "feat/919-unlanded") {
		t.Error("the unmerged branch was deleted")
	}
	entries, _, _ := ReadFixLog(fx.Log.Path)
	if len(entries) != 2 {
		t.Errorf("fix log has %d entries, want 2 (stash, branch)", len(entries))
	}
}

func TestHygieneRemedies_BranchDeleteRefusesAMovedTip(t *testing.T) {
	r := hygieneFixture(t)
	fx := hygieneFixer(t, r.dir, verbDeps{}, "stranded_branches")
	var f Finding
	for _, x := range fx.Scan(context.Background())[0].Findings {
		if x.Evidence["branch"] == "fix/912-landed" {
			f = x
		}
	}
	f.Evidence["tip"] = strings.Repeat("0", 40) // the scan saw a different tip
	v, _ := fx.Verbs.Lookup(verbBranchDelete)
	if err := v.Precondition(context.Background(), f); err == nil {
		t.Fatal("a branch whose tip moved since the scan passed the precondition")
	}
}

func TestHygieneRemedies_WipPruneLandedAndRefusesUnlanded(t *testing.T) {
	r := newLeakRepo(t)
	unlanded := preserveWip(r, "feat/2001-unlanded", 2001, "feature-dev", 1787939337)
	landed := preserveWip(r, "feat/2002-landed", 2002, "feature-dev", 1787939338)
	// Land the second ref's content on main (squash shape).
	r.write("unfinished.txt", "half-done\n")
	r.git("add", ".")
	r.git("commit", "-m", "squash: landed")
	r.git("update-ref", "refs/remotes/origin/main", strings.TrimSpace(r.git("rev-parse", "main")))
	// The first ref's content differs from main now.
	r.git("update-ref", unlanded, strings.TrimSpace(func() string {
		r.git("checkout", "-q", "-b", "tmp-2001", "main")
		r.write("other.txt", "not landed\n")
		r.git("add", ".")
		r.git("commit", "-q", "-m", "wip\n\nRefs: #2001\nNightgauge-WIP: feature-dev")
		sha := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "main")
		return sha
	}()))

	fx := hygieneFixer(t, r.dir, verbDeps{}, "preserved_wip")
	rep := fx.Run(context.Background(), FixOptions{Yes: true})
	if got := resultFor(t, rep, "NGD020", "ref", landed); got.Outcome != OutcomeFixed {
		t.Errorf("landed ref: %s (%s)", got.Outcome, got.Detail)
	}
	if got := resultFor(t, rep, "NGD020", "ref", unlanded); got.Outcome != OutcomeBlocked {
		t.Errorf("unlanded ref: %s (%s), want blocked", got.Outcome, got.Detail)
	}
	if out := r.git("for-each-ref", "--format=%(refname)", reclaim.WipRefNamespace); !strings.Contains(out, unlanded) || strings.Contains(out, landed) {
		t.Errorf("refs after prune: %q", out)
	}
}

// fakeProcesses is an injectable process table for the terminate verbs.
type fakeProcesses struct {
	command  atomic.Value // string
	uid      int
	signaled atomic.Int32
	onSignal func()
}

func (p *fakeProcesses) deps() verbDeps {
	return verbDeps{
		exitWait: 200 * time.Millisecond,
		lookup: func(pid int) (processIdentity, error) {
			c, _ := p.command.Load().(string)
			if c == "" {
				return processIdentity{}, fmt.Errorf("pid %d gone", pid)
			}
			return processIdentity{Command: c, UID: p.uid}, nil
		},
		signal: func(int) error {
			p.signaled.Add(1)
			if p.onSignal != nil {
				p.onSignal()
			}
			return nil
		},
	}
}

// TestOrphanedProcessRemedySafety_ApplyTime: the finding named the pid, but by
// apply time it runs something else; the verb refuses and never signals.
func TestOrphanedProcessRemedySafety_ApplyTime(t *testing.T) {
	if os.Getuid() < 0 {
		t.Skip("no uid on this platform")
	}
	const pid = 987654
	f := newFinding("orphaned_processes", "NGD021", SeverityHousekeeping, "orphan", "c",
		map[string]string{"pid": strconv.Itoa(pid), "command": "/usr/local/bin/nightgauge serve"},
		[]string{strconv.Itoa(pid)},
		Remedy{ID: "terminate", Kind: RemedyConfirm, Verb: verbProcessTerminate, Verify: "orphaned_processes"})
	procs := &fakeProcesses{uid: os.Getuid()}
	v, _ := builtinVerbs(&Env{}, procs.deps()).Lookup(verbProcessTerminate)

	for _, cmd := range []string{"/bin/zsh -l", "/usr/local/bin/nightgauge run --issue 9"} {
		procs.command.Store(cmd)
		if err := v.Precondition(context.Background(), f); err == nil {
			t.Errorf("pid now running %q passed the precondition", cmd)
		}
	}
	procs.command.Store("/usr/local/bin/nightgauge serve")
	if err := v.Precondition(context.Background(), f); err != nil {
		t.Fatalf("the named nightgauge pid was refused: %v", err)
	}
	procs.onSignal = func() { procs.command.Store("") }
	if err := v.Apply(context.Background(), f); err != nil || procs.signaled.Load() != 1 {
		t.Fatalf("apply: err %v, signals %d", err, procs.signaled.Load())
	}
}

func TestHygieneRemedies_ServeLeaseReclaim(t *testing.T) {
	if !flock.Supported {
		t.Skip("no advisory file lock on this platform")
	}
	if os.Getuid() < 0 {
		t.Skip("no uid on this platform")
	}
	isolateMachineState(t)
	root := t.TempDir()
	now := time.Now()
	lease, err := runstate.AcquireServeLease(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(lease.Release)
	if err := runstate.WriteServeSidecar(root, runstate.ServeSidecar{
		PID: 4242, StartedAt: now.Add(-5 * time.Hour),
		LastHeartbeatAt: now.Add(-runstate.ServeLeaseStaleAfter - time.Hour),
	}); err != nil {
		t.Fatalf("WriteServeSidecar: %v", err)
	}
	procs := &fakeProcesses{uid: os.Getuid()}
	procs.command.Store("/usr/local/bin/nightgauge serve")
	// The wedged holder exits on SIGTERM, which releases its lease.
	procs.onSignal = func() { procs.command.Store(""); lease.Release() }
	env := &Env{Cwd: root, Now: now}
	fx := &Fixer{Registry: registryOf(t, "serve_lease"), Verbs: builtinVerbs(env, procs.deps()), Env: env}

	rep := fx.Run(context.Background(), FixOptions{})
	if got := onlyResult(t, rep); got.Action != ActionAwaitingConsent || procs.signaled.Load() != 0 {
		t.Fatalf("reclaim without --yes: %s, signals %d", got.Action, procs.signaled.Load())
	}
	rep = fx.Run(context.Background(), FixOptions{Yes: true})
	if got := onlyResult(t, rep); got.Outcome != OutcomeFixed || procs.signaled.Load() != 1 {
		t.Fatalf("reclaim: %s (%s), signals %d", got.Outcome, got.Detail, procs.signaled.Load())
	}
}

func TestComplexityModelRemedyPreview_EndToEnd(t *testing.T) {
	root := t.TempDir()
	modelPath := filepath.Join(root, ".nightgauge", "complexity-model.yaml")
	fx := hygieneFixer(t, root, verbDeps{}, "complexity_model")
	fx.Env.Now = time.Now()

	dry := onlyResult(t, fx.Run(context.Background(), FixOptions{DryRun: true, Yes: true}))
	if dry.Action != ActionPreviewed || !strings.Contains(dry.Preview, modelPath) {
		t.Fatalf("dry run = %s %q, want a preview naming %s", dry.Action, dry.Preview, modelPath)
	}
	if _, err := os.Stat(modelPath); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote %s", modelPath)
	}
	got := onlyResult(t, fx.Run(context.Background(), FixOptions{Yes: true}))
	if got.Outcome != OutcomeFixed {
		t.Fatalf("outcome.init: %s (%s)", got.Outcome, got.Detail)
	}
	info, err := os.Stat(modelPath)
	if err != nil {
		t.Fatalf("the model was not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("model mode %o, want 0600", info.Mode().Perm())
	}
	if d, err := os.Stat(filepath.Dir(modelPath)); err != nil {
		t.Errorf("stat created directory: %v", err)
	} else if d.Mode().Perm() != 0o700 {
		t.Errorf("created directory mode %o, want 0700", d.Mode().Perm())
	}
	// Idempotent: the model now exists, so a second pass has nothing to do.
	if rep := fx.Run(context.Background(), FixOptions{Yes: true}); len(rep.Results) != 0 {
		t.Errorf("second pass: %d result(s)", len(rep.Results))
	}
}

// writeRunSnapshot persists a non-terminal runtime snapshot for issue through
// the state package's own writer, in the directory the sweep reads.
func writeRunSnapshot(t *testing.T, root string, issue int) {
	t.Helper()
	runID, err := runstate.NewRunID()
	if err != nil {
		t.Fatalf("mint run id: %v", err)
	}
	rs := state.NewRuntimeState("owner/repo", issue, "item-"+strconv.Itoa(issue), runID)
	rs.SetProcess(os.Getpid(), filepath.Join(root, ".worktrees", "issue-"+strconv.Itoa(issue)))
	if err := rs.Persist(state.PipelineStateDir(root)); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
}
