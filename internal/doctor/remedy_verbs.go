package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The built-in remedy verbs: the closed registry ADR-025 § 3 names. Each verb
// reads only the finding's evidence, re-derives the scan's claim in its
// Precondition, and acts through the same Go code the matching CLI command
// uses. Nothing here builds a shell string.
//
// A declared verb deliberately left unregistered, so the engine fails closed
// on it: `repo.init` (interactive by nature; the interactive CLI owns it).
// `automation.restart` is registered (automation_restart.go) and reaches the
// autonomous scheduler only through the daemon's own start method.

// verbDeps are the process-level effects a verb has, injectable for tests.
type verbDeps struct {
	lookup   processLookup           // nil → ps
	signal   func(pid int) error     // nil → SIGTERM to the single pid
	exitWait time.Duration           // how long terminate waits for the pid to go
	teardown func(name string) error // nil → dockercompose.TeardownProject
}

// defaultExitWait bounds how long a terminate verb waits for its pid to exit
// before handing the answer to verification.
const defaultExitWait = 5 * time.Second

// BuiltinVerbs returns the registry of every built-in verb, bound to env (its
// working directory, GitHub client, config and clock).
func BuiltinVerbs(env *Env) *VerbRegistry {
	return builtinVerbs(env, verbDeps{})
}

func builtinVerbs(env *Env, deps verbDeps) *VerbRegistry {
	if deps.lookup == nil {
		deps.lookup = psProcessLookup
	}
	if deps.signal == nil {
		deps.signal = sigterm
	}
	if deps.exitWait <= 0 {
		deps.exitWait = defaultExitWait
	}
	v := &verbs{env: env, deps: deps}
	reg := NewVerbRegistry()
	must := func(name string, pre, apply func(context.Context, Finding) error) {
		if err := reg.Register(name, VerbFuncs{
			PreviewFunc:      declaredPreview(name),
			PreconditionFunc: pre,
			ApplyFunc:        apply,
		}); err != nil {
			panic(err)
		}
	}
	must(verbWorktreeSweep, v.worktreePrecondition, v.worktreeApply)
	must(verbStashSweep, v.stashPrecondition, v.stashApply)
	must(verbBranchDelete, v.branchPrecondition, v.branchApply)
	must(verbWipPrune, v.wipPrecondition, v.wipApply)
	must(verbProcessTerminate, v.terminatePrecondition, v.terminateApply)
	must(verbServeLeaseReclaim, v.leasePrecondition, v.leaseApply)
	must(verbComposeCleanup, v.composePrecondition, v.composeApply)
	must(verbOutcomeInit, v.outcomeInitPrecondition, v.outcomeInitApply)
	must(verbSurvivalSweep, v.survivalPrecondition, v.survivalApply)
	must(verbBuildCLI, v.buildCLIPrecondition, v.buildCLIApply)
	must(verbGHAuthRefresh, v.authRefreshPrecondition, v.authRefreshApply)
	must(verbAutomationRestart, v.restartPrecondition, v.restartApply)
	if err := reg.Register(verbLayoutMigrate, layoutMigrateVerb(newLayoutMigrator, newMachineStateMigrator)); err != nil {
		panic(err)
	}
	return reg
}

type verbs struct {
	env  *Env
	deps verbDeps
}

// declaredPreview returns the preview the finding's check declared for this
// verb: the checks compose it from the evidence at scan time, and it is what
// `--dry-run` prints.
func declaredPreview(verb string) func(context.Context, Finding) (string, error) {
	return func(_ context.Context, f Finding) (string, error) {
		for _, r := range f.Remedies {
			if r.Verb == verb {
				return r.Preview, nil
			}
		}
		return "", fmt.Errorf("finding %s declares no %s remedy", f.Code, verb)
	}
}

// evidence returns a required evidence value. A finding missing it cannot be
// acted on: the verb has nothing to re-derive.
func evidence(f Finding, key string) (string, error) {
	v := strings.TrimSpace(f.Evidence[key])
	if v == "" {
		return "", fmt.Errorf("finding %s carries no %q evidence", f.Code, key)
	}
	return v, nil
}

// doors is the merged-PR second door (#916) from doctor's own client, the one
// the scan used, so the precondition proves merged-ness by the same rules.
func (v *verbs) doors(ctx context.Context) mergedPRDoorFactory {
	return mergedPRDoor(ctx, v.env.Client)
}

func (v *verbs) door(ctx context.Context, root string) execution.MergedPRLookup {
	return doorFor(v.doors(ctx), root)
}

// --- worktree.sweep ---------------------------------------------------------

// worktreeSweepPlan re-classifies the root's worktrees exactly as `nightgauge
// worktree sweep` does — with the root's in-flight set — and reports whether
// path is still reclaimable. The returned exclusion set holds every OTHER
// reclaimable worktree's issue, so Apply removes this finding's worktree
// only.
func (v *verbs) worktreeSweepPlan(ctx context.Context, f Finding) (main string, exclude map[int]bool, protected map[int]string, err error) {
	root, err := evidence(f, "repo_root")
	if err != nil {
		return "", nil, nil, err
	}
	path, err := evidence(f, "path")
	if err != nil {
		return "", nil, nil, err
	}
	main = config.MainCheckoutRoot(root)
	if main == "" {
		return "", nil, nil, fmt.Errorf("%s is no longer a git repository", root)
	}
	active, err := state.ActiveIssuesForRoot(main)
	if err != nil {
		// "I could not look" is never "nothing is running" (#296).
		return "", nil, nil, fmt.Errorf("in-flight set for %s is unreadable, refusing to sweep blind: %w", main, err)
	}
	res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
		RepoRoot: main, ActiveIssues: active.Issues, Protected: active.Protected,
		DryRun: true, MergedPRLookup: v.door(ctx, main),
	})
	if err != nil {
		return "", nil, nil, err
	}
	exclude = map[int]bool{}
	protected = map[int]string{}
	for k, val := range active.Issues {
		exclude[k] = val
	}
	for k, val := range active.Protected {
		protected[k] = val
	}
	found := false
	for _, r := range res.Reclaimed {
		if r.Path == path {
			found = true
			continue
		}
		exclude[r.IssueNumber] = true
		protected[r.IssueNumber] = "doctor: not the selected finding"
	}
	if !found {
		for _, s := range res.Skipped {
			if s.Path == path {
				return "", nil, nil, fmt.Errorf("worktree %s is no longer reclaimable: %s %s", path, s.Reason, s.ReasonDetail)
			}
		}
		return "", nil, nil, fmt.Errorf("worktree %s is no longer registered in %s", path, main)
	}
	if n, ok := execution.IssueNumberFromWorktreeDir(filepath.Base(path)); ok && exclude[n] {
		return "", nil, nil, fmt.Errorf("another reclaimable worktree shares issue #%d with %s; sweep %s with `nightgauge worktree sweep`", n, path, main)
	}
	return main, exclude, protected, nil
}

func (v *verbs) worktreePrecondition(ctx context.Context, f Finding) error {
	_, _, _, err := v.worktreeSweepPlan(ctx, f)
	return err
}

func (v *verbs) worktreeApply(ctx context.Context, f Finding) error {
	main, exclude, protected, err := v.worktreeSweepPlan(ctx, f)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	res, err := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
		RepoRoot: main, ActiveIssues: exclude, Protected: protected, MergedPRLookup: v.door(ctx, main),
	})
	if err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return errors.New(strings.Join(res.Errors, "; "))
	}
	return nil
}

// --- stash.sweep --------------------------------------------------------------

func (v *verbs) stashPrecondition(ctx context.Context, f Finding) error {
	root, err := evidence(f, "repo_root")
	if err != nil {
		return err
	}
	message, err := evidence(f, "message")
	if err != nil {
		return err
	}
	branch := f.Evidence["branch"]
	if !landedBranches(root, v.doors(ctx))[branch] {
		return fmt.Errorf("branch %q is no longer proven landed, so restoring the stash could collide with unlanded work", branch)
	}
	res, err := reclaim.SweepPipelineStashes(reclaim.StashSweepOptions{RepoRoot: root, Message: message, DryRun: true})
	if err != nil {
		return err
	}
	if err := stashSelected(res, message); err != nil {
		return err
	}
	out, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("read the working tree of %s: %w", root, err)
	}
	if reclaim.ClassifyStatus(string(out)).Blocked() {
		return fmt.Errorf("the working tree of %s has uncommitted changes; the stash is restored only onto a clean tree", root)
	}
	return nil
}

func stashSelected(res reclaim.StashSweepResult, message string) error {
	for _, r := range res.Reclaimed {
		if r.Message == message {
			return nil
		}
	}
	for _, s := range res.Skipped {
		if s.Message == message {
			return fmt.Errorf("stash %s (%q) would not be restored: %s", s.Ref, message, s.Reason)
		}
	}
	return fmt.Errorf("stash %q is no longer on the stash stack", message)
}

func (v *verbs) stashApply(_ context.Context, f Finding) error {
	root, _ := evidence(f, "repo_root")
	message, _ := evidence(f, "message")
	res, err := reclaim.SweepPipelineStashes(reclaim.StashSweepOptions{RepoRoot: root, Message: message})
	if err != nil {
		return err
	}
	if err := stashSelected(res, message); err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	if len(res.Errors) > 0 {
		return errors.New(strings.Join(res.Errors, "; "))
	}
	return nil
}

// --- branch.delete ------------------------------------------------------------

func (v *verbs) branchPrecondition(ctx context.Context, f Finding) error {
	root, err := evidence(f, "repo_root")
	if err != nil {
		return err
	}
	branch, err := evidence(f, "branch")
	if err != nil {
		return err
	}
	tip, err := evidence(f, "tip")
	if err != nil {
		return err
	}
	if err := branchDeletePrecondition(root, branch, v.door(ctx, root)); err != nil {
		return err
	}
	cur, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return fmt.Errorf("branch %s no longer resolves in %s", branch, root)
	}
	if got := strings.TrimSpace(string(cur)); got != tip {
		return fmt.Errorf("branch %s moved since the scan (%s, was %s)", branch, shortSHA(got), shortSHA(tip))
	}
	return nil
}

// branchApply deletes the ref only if it still points at the tip the proof
// was derived for: `update-ref -d <ref> <old>` is git's compare-and-delete.
func (v *verbs) branchApply(_ context.Context, f Finding) error {
	root, _ := evidence(f, "repo_root")
	branch, _ := evidence(f, "branch")
	tip, _ := evidence(f, "tip")
	if out, err := exec.Command("git", "-C", root, "update-ref", "-d", "refs/heads/"+branch, tip).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: delete %s: %v: %s", ErrRemedyBlocked, branch, err, strings.TrimSpace(string(out)))
	}
	// The branch's tracking config goes with it, as `git branch -D` would do.
	_ = exec.Command("git", "-C", root, "config", "--remove-section", "branch."+branch).Run()
	return nil
}

// --- wip.prune ----------------------------------------------------------------

func (v *verbs) wipPlan(f Finding, dryRun bool) error {
	root, err := evidence(f, "repo_root")
	if err != nil {
		return err
	}
	ref, err := evidence(f, "ref")
	if err != nil {
		return err
	}
	commit := f.Evidence["commit"]
	res, err := reclaim.PruneWipRefs(reclaim.WipPruneOptions{RepoRoot: root, Ref: ref, DryRun: dryRun})
	if err != nil {
		return err
	}
	for _, p := range res.Pruned {
		if p.Ref == ref {
			if commit != "" && p.Commit != commit {
				return fmt.Errorf("%s now points at %s, not the scanned %s", ref, shortSHA(p.Commit), shortSHA(commit))
			}
			return nil
		}
	}
	for _, k := range res.Kept {
		if k.Ref == ref {
			return fmt.Errorf("%s is kept (%s): its content has not landed in %s, and prune removes only landed work", ref, k.Reason, res.BaseRef)
		}
	}
	return fmt.Errorf("%s no longer exists in %s", ref, root)
}

func (v *verbs) wipPrecondition(_ context.Context, f Finding) error { return v.wipPlan(f, true) }

func (v *verbs) wipApply(_ context.Context, f Finding) error {
	if err := v.wipPlan(f, false); err != nil {
		return fmt.Errorf("%w: %v", ErrRemedyBlocked, err)
	}
	return nil
}

// --- process.terminate and serve_lease.reclaim ---------------------------------

func findingPID(f Finding) (int, error) {
	raw, err := evidence(f, "pid")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("finding %s names an unusable pid %q", f.Code, raw)
	}
	return pid, nil
}

// terminatePrecondition re-reads the pid: still a nightgauge binary owned by
// this user, and still running the command the finding named.
func (v *verbs) terminatePrecondition(_ context.Context, f Finding) error {
	pid, err := findingPID(f)
	if err != nil {
		return err
	}
	if err := terminatePrecondition(pid, v.deps.lookup); err != nil {
		return err
	}
	if want := f.Evidence["command"]; want != "" {
		id, err := v.deps.lookup(pid)
		if err != nil {
			return err
		}
		if id.Command != want {
			return fmt.Errorf("pid %d now runs %q, not the scanned %q; the pid may have been reused", pid, id.Command, want)
		}
	}
	return nil
}

func (v *verbs) terminateApply(_ context.Context, f Finding) error {
	pid, err := findingPID(f)
	if err != nil {
		return err
	}
	return v.signalAndWait(pid, f.Evidence["command"])
}

// signalAndWait sends SIGTERM to the single pid — never its process group —
// and waits a bounded time for it to go. It never escalates: a pid still
// running is reported by verification as still present.
func (v *verbs) signalAndWait(pid int, command string) error {
	if err := v.deps.signal(pid); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(v.deps.exitWait)
	for time.Now().Before(deadline) {
		id, err := v.deps.lookup(pid)
		if err != nil || (command != "" && id.Command != command) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func sigterm(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// leasePrecondition re-inspects the lease: still held by the same pid, still
// the same claim (start time), still stale, and the pid is still a nightgauge
// binary of this user.
func (v *verbs) leasePrecondition(_ context.Context, f Finding) error {
	ws, err := evidence(f, "workspace")
	if err != nil {
		return err
	}
	pid, err := findingPID(f)
	if err != nil {
		return err
	}
	holder, held := runstate.InspectServeLease(ws)
	switch {
	case !held:
		return fmt.Errorf("the serve lease for %s is no longer held", ws)
	case !holder.Known:
		return fmt.Errorf("the serve lease for %s is held but its claim record cannot be read", ws)
	case holder.PID != pid:
		return fmt.Errorf("the serve lease for %s is now held by pid %d, not %d", ws, holder.PID, pid)
	case f.Evidence["started_at"] != "" && holder.StartedAt.Format(time.RFC3339) != f.Evidence["started_at"]:
		return fmt.Errorf("pid %d's lease claim restarted since the scan", pid)
	case !holder.Stale:
		return fmt.Errorf("pid %d is heartbeating again; the lease is no longer wedged", pid)
	}
	return terminatePrecondition(pid, v.deps.lookup)
}

func (v *verbs) leaseApply(_ context.Context, f Finding) error {
	pid, err := findingPID(f)
	if err != nil {
		return err
	}
	id, err := v.deps.lookup(pid)
	if err != nil {
		return fmt.Errorf("%w: pid %d is gone", ErrRemedyBlocked, pid)
	}
	return v.signalAndWait(pid, id.Command)
}

// --- compose.cleanup ------------------------------------------------------------

func (v *verbs) composePrecondition(ctx context.Context, f Finding) error {
	name, err := evidence(f, "project")
	if err != nil {
		return err
	}
	orphans, determined := findOrphanedComposeProjects(ctx, v.env.Cwd)
	if !determined {
		return errors.New("the active worktree set could not be read, so every stack would look orphaned")
	}
	for _, p := range orphans {
		if p.Name == name {
			return nil
		}
	}
	return fmt.Errorf("compose project %s is no longer orphaned (or no longer exists)", name)
}

func (v *verbs) composeApply(ctx context.Context, f Finding) error {
	name, _ := evidence(f, "project")
	if v.deps.teardown != nil {
		return v.deps.teardown(name)
	}
	res, err := dockercompose.TeardownProject(ctx, name, dockercompose.TeardownOptions{})
	if err != nil {
		return err
	}
	if res.Skipped {
		return fmt.Errorf("%w: %s", ErrRemedyBlocked, res.SkipReason)
	}
	return nil
}

// --- outcome.init -----------------------------------------------------------------

// modelTarget is the only path outcome.init writes: the workspace's own model,
// re-derived from the working directory, never taken from the finding.
func (v *verbs) modelTarget(f Finding) (string, error) {
	root := v.env.Cwd
	if root == "" {
		return "", errors.New("no workspace root")
	}
	target, err := complexityModelPath(root)
	if err != nil {
		return "", err
	}
	if p := f.Evidence["path"]; p != target {
		return "", fmt.Errorf("the finding names %q, not this workspace's model %s", p, target)
	}
	return target, nil
}

func (v *verbs) outcomeInitPrecondition(_ context.Context, f Finding) error {
	target, err := v.modelTarget(f)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Dir(target)); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", filepath.Dir(target))
	}
	info, err := os.Lstat(target)
	switch f.Code {
	case codeComplexityModelMissing:
		if err == nil {
			return fmt.Errorf("%s exists now; nothing is overwritten", target)
		}
		if !os.IsNotExist(err) {
			return err
		}
	default:
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s is no longer the invalid regular file the scan found", target)
		}
		if gh.NewOutcomeService(v.env.Cwd).ValidateModel() == nil {
			return fmt.Errorf("%s is valid now; nothing is replaced", target)
		}
	}
	return nil
}

func (v *verbs) outcomeInitApply(_ context.Context, f Finding) error {
	target, err := v.modelTarget(f)
	if err != nil {
		return err
	}
	if f.Code != codeComplexityModelMissing {
		// Move the invalid model aside rather than deleting it: the preview
		// says so, and the operator may want what was in it.
		aside := fmt.Sprintf("%s.invalid-%d", target, time.Now().Unix())
		if err := os.Rename(target, aside); err != nil {
			return fmt.Errorf("move the invalid model aside: %w", err)
		}
	}
	_, err = gh.NewOutcomeService(v.env.Cwd).InitializeModel()
	return err
}

// --- survival.sweep ----------------------------------------------------------------

func (v *verbs) survivalPrecondition(_ context.Context, _ Finding) error {
	if v.env.Cwd == "" {
		return errors.New("no workspace root")
	}
	pending, err := survival.NewStore(v.env.Cwd).Pending()
	if err != nil {
		return fmt.Errorf("the survival store cannot be read: %w", err)
	}
	if len(pending) == 0 {
		return errors.New("no pending survival records remain")
	}
	return nil
}

func (v *verbs) survivalApply(ctx context.Context, _ Finding) error {
	window := survival.DefaultWindowDays
	if v.env.Cfg != nil {
		window = v.env.Cfg.Pipeline.ResolveSurvivalWindowDays()
	}
	_, err := gh.FinalizeDueSurvivalRecords(ctx, v.env.Cwd, time.Now(), window)
	return err
}

// --- binary.build_cli -----------------------------------------------------------

func (v *verbs) buildCLIPrecondition(_ context.Context, _ Finding) error {
	if sourceCheckoutRoot(v.env.Cwd) == "" {
		return errors.New("the working directory is no longer inside a nightgauge source checkout")
	}
	if _, err := exec.LookPath("make"); err != nil {
		return errors.New("`make` is not on PATH")
	}
	return nil
}

// buildCLIApply runs the checkout's own `make build-cli`, a fixed argv in the
// checkout the precondition re-derived.
func (v *verbs) buildCLIApply(ctx context.Context, _ Finding) error {
	checkout := sourceCheckoutRoot(v.env.Cwd)
	if checkout == "" {
		return fmt.Errorf("%w: no source checkout", ErrRemedyBlocked)
	}
	cmd := exec.CommandContext(ctx, "make", "build-cli")
	cmd.Dir = checkout
	if out, err := cmd.CombinedOutput(); err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("make build-cli: %w: %s", err, tail)
	}
	return nil
}
