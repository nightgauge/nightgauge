// Package gaterun runs a repository's local gate with at most one gate per
// worktree at a time (#3207).
//
// A guard file in the clone's pipeline state directory, keyed on the
// worktree, names the running gate. A second gate-run on the same tree waits
// for it and reports its result. A gate-run on a different tree supersedes it:
// the running gate's process group is stopped and verified dead, the ledger
// records it as superseded, and the new gate runs. Gates in other worktrees
// are untouched, because concurrent gates across worktrees are a supported
// capability (#855, #1983).
package gaterun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/gateledger"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/procstart"
)

// Exit codes of a guarded gate-run, beside the gate's own.
const (
	// ExitHeld means another gate holds this worktree on a different tree and
	// could not be verified stopped, so nothing ran.
	ExitHeld = 75
	// ExitSuperseded means this gate was stopped because its worktree's tree
	// changed. It is not a failure; the superseder recorded it.
	ExitSuperseded = 76
)

// EnvRunID carries the guarded gate's ledger run id to the gate's steps, so a
// gate that records itself (scripts/ci-local.sh) records under the same id.
const EnvRunID = "NIGHTGAUGE_GATE_RUN_ID"

// Options configures one guarded run.
type Options struct {
	// Root is a directory in the worktree whose gate runs.
	Root string
	// Tree is the worktree's fingerprint at the start.
	Tree string
	// Kind is the gate's ledger kind.
	Kind string
	// RecordsItself is true when the gate writes its own start and finish
	// events; otherwise Run records them.
	RecordsItself bool
	// Run runs the gate, handing each step's prepared command to runStep.
	Run func(ctx context.Context, runStep func(*exec.Cmd) error) error
	// Record completes and appends a ledger event.
	Record func(gateledger.Event) error
	// Stderr receives gate-run's own lines.
	Stderr io.Writer

	// Poll is how often a waiting gate-run checks the holder (default 2s).
	Poll time.Duration
	// TermGrace is how long a superseded group has after SIGTERM before
	// SIGKILL (default 10s); KillGrace how long after SIGKILL before giving
	// up with ExitHeld (default 5s).
	TermGrace, KillGrace time.Duration
	// Stop signals a process group (SIGKILL when force); GroupAlive reports
	// whether one exists. Tests replace them; the defaults are the OS's.
	Stop       func(pgid int, force bool) error
	GroupAlive func(pgid int) bool

	// Timeout is the longest a gate may run (hooks.pre_push.timeout_seconds,
	// default 30m). A guard older than it is stale, and a same-tree wait
	// gives up after it.
	Timeout time.Duration
	// Now and ProcStart are test seams: the clock, and a process's start
	// time (internal/procstart.Start).
	Now       func() time.Time
	ProcStart func(pid int) (int64, error)
	// Signals delivers the signals that stop this gate-run. When nil, Run
	// subscribes to SIGINT, SIGTERM and SIGHUP itself.
	Signals <-chan os.Signal
}

// DefaultTimeout bounds a gate when Options.Timeout is unset.
const DefaultTimeout = 30 * time.Minute

func (o *Options) defaults() {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	if o.TermGrace <= 0 {
		o.TermGrace = 10 * time.Second
	}
	if o.KillGrace <= 0 {
		o.KillGrace = 5 * time.Second
	}
	if o.Stop == nil {
		o.Stop = stopGroup
	}
	if o.GroupAlive == nil {
		o.GroupAlive = groupAlive
	}
	if o.Record == nil {
		o.Record = func(gateledger.Event) error { return nil }
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ProcStart == nil {
		o.ProcStart = procstart.Start
	}
}

// guard is the guard file: the gate holding a worktree. PIDStart and
// PGIDStart are the start times of the holder and of its step's group leader,
// so a reused pid is never taken for the holder (0 where unsupported).
type guard struct {
	PID       int    `json:"pid"`
	PIDStart  int64  `json:"pid_start,omitempty"`
	Host      string `json:"host,omitempty"`
	PGID      int    `json:"pgid,omitempty"`
	PGIDStart int64  `json:"pgid_start,omitempty"`
	Tree      string `json:"tree"`
	RunID     string `json:"run_id"`
	Kind      string `json:"kind"`
	Worktree  string `json:"worktree"`
	StartedAt string `json:"started_at"`
}

// result is the last guarded gate's outcome, for a gate-run that waited on it.
type result struct {
	RunID    string `json:"run_id"`
	Tree     string `json:"tree"`
	ExitCode int    `json:"exit_code"`
}

// supersedeMarker tells the holder RunID that tree Tree superseded it. It is a
// separate file so the holder's guard updates never race the superseder.
type supersedeMarker struct {
	RunID string `json:"run_id"`
	Tree  string `json:"tree"`
	PID   int    `json:"pid"`
}

type paths struct{ guard, result, marker, worktree string }

func pathsFor(root string) (paths, error) {
	wt := toplevel(root)
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		return paths{}, fmt.Errorf("gaterun: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return paths{}, fmt.Errorf("gaterun: %w", err)
	}
	sum := sha256.Sum256([]byte(wt))
	base := filepath.Join(dir, "gate-run-"+hex.EncodeToString(sum[:])[:12])
	return paths{guard: base + ".json", result: base + ".result.json", marker: base + ".supersede.json", worktree: wt}, nil
}

// toplevel is the worktree's resolved top-level directory: the guard's key.
func toplevel(root string) string {
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(out))
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

// HeldError is the ExitHeld case: the holder named here could not be stopped.
type HeldError struct {
	PID      int
	Tree     string
	Worktree string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("gate-run: gate pid %d on tree %s is still running in %s", e.PID, e.Tree, e.Worktree)
}

const maxAttempts = 20

// Run runs the gate under the worktree's guard and returns its exit code:
// the gate's own, a waited-for gate's, ExitHeld or ExitSuperseded. An error
// means gate-run itself could not run.
func Run(ctx context.Context, o Options) (int, error) {
	o.defaults()
	if o.Run == nil {
		return 1, errors.New("gaterun: no gate to run")
	}
	p, err := pathsFor(o.Root)
	if err != nil {
		return 1, err
	}
	runID, err := newID()
	if err != nil {
		return 1, err
	}
	if o.Signals == nil {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, gateSignals...)
		defer signal.Stop(ch)
		o.Signals = ch
	}
	g := guard{PID: os.Getpid(), Host: hostname(), Tree: o.Tree, RunID: runID, Kind: o.Kind, Worktree: p.worktree}
	g.PIDStart, _ = o.ProcStart(g.PID)
	for attempt := 0; ; attempt++ {
		if attempt >= maxAttempts {
			return 1, fmt.Errorf("gaterun: could not take the guard %s after %d attempts", p.guard, attempt)
		}
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		// Nothing is held yet, so a signal has nothing to release.
		select {
		case sig := <-o.Signals:
			fmt.Fprintf(o.Stderr, "gate-run: interrupted by %v before the gate started\n", sig)
			return 128 + signum(sig), nil
		default:
		}
		g.StartedAt = o.Now().UTC().Format(time.RFC3339Nano)
		took, err := acquire(p.guard, g)
		if err != nil {
			return 1, err
		}
		if took {
			break
		}
		holder, err := readJSON[guard](p.guard)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !o.holderLive(holder) {
			o.reclaim(p, holder, err == nil)
			continue
		}
		if holder.Tree == o.Tree {
			if code, ok, err := o.waitFor(ctx, p, holder); err != nil || ok {
				return code, err
			}
			continue
		}
		if err := o.supersede(p, holder); err != nil {
			var held *HeldError
			if errors.As(err, &held) {
				fmt.Fprintln(o.Stderr, held.Error())
				return ExitHeld, nil
			}
			return 1, err
		}
	}
	defer release(p.guard, runID)
	// A marker for an earlier holder is spent. One naming this run was written
	// by a newer tree that read this guard already, and must survive.
	if m, err := readJSON[supersedeMarker](p.marker); err == nil && m.RunID != runID {
		removeMarkerFor(p.marker, m.RunID)
	}
	return o.runHeld(ctx, p, g)
}

// acquire creates the guard atomically: the content is written to a private
// file first and linked into place, so no reader sees a partial guard and an
// existing guard is never replaced.
func acquire(path string, g guard) (bool, error) {
	tmp, err := writeTemp(path, g)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("gaterun: take guard: %w", err)
	}
	return true, nil
}

// holderLive reports whether the guard's holder is still the gate that wrote
// it. A guard older than Timeout is stale: no gate may run that long, so it is
// never a fresh-holder TTL (#1697). On another host only age can be judged.
// Otherwise the pid must exist and, where start times are recorded, still
// have the holder's start time: a pid that died and was reused, or a guard
// that survived a reboot, is stale. EPERM counts as existing only because the
// start time then decides.
func (o *Options) holderLive(g guard) bool {
	if started, err := time.Parse(time.RFC3339Nano, g.StartedAt); err == nil &&
		o.Now().Sub(started) >= o.Timeout {
		return false
	}
	if g.Host != "" && g.Host != hostname() {
		return true
	}
	if g.PID <= 0 || !pidAlive(g.PID) {
		return false
	}
	if g.PIDStart == 0 {
		return true
	}
	start, err := o.ProcStart(g.PID)
	return err == nil && start == g.PIDStart
}

// ownGroup reports whether group pgid is still the step group whose leader
// started at pgidStart, so a reused pgid is never signalled. A leader that
// has exited while its group lives on still identifies the group: a pid is
// not reused while a process group of that id exists.
func (o *Options) ownGroup(pgid int, pgidStart int64) bool {
	if pgid <= 0 || !o.GroupAlive(pgid) {
		return false
	}
	start, err := o.ProcStart(pgid)
	if err != nil {
		return true
	}
	return pgidStart != 0 && start == pgidStart
}

// reclaim removes a guard whose holder is gone or older than Timeout. A step
// group the holder left running is stopped first, so two gates never overlap,
// but only while its leader's start time proves it is that group. The guard
// is moved aside before removal and restored when it turns out to be a fresh
// one another gate-run took meanwhile.
func (o *Options) reclaim(p paths, holder guard, parsed bool) {
	if parsed && o.ownGroup(holder.PGID, holder.PGIDStart) {
		o.stopUntilDead([]int{holder.PGID}, func() bool { return true })
	}
	aside := fmt.Sprintf("%s.stale-%d-%d", p.guard, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(p.guard, aside); err != nil {
		return
	}
	got, err := readJSON[guard](aside)
	if parsed && err == nil && got.RunID != holder.RunID {
		_ = os.Link(aside, p.guard)
	}
	_ = os.Remove(aside)
}

// waitFor waits for a live holder on the same tree and returns its result.
// ok is false when the holder ended without one, so the caller runs the gate.
// The wait ends by the holder's own deadline (its start plus Timeout), and
// never later than Timeout from now: past it, it is ExitHeld naming the guard.
func (o *Options) waitFor(ctx context.Context, p paths, holder guard) (int, bool, error) {
	fmt.Fprintf(o.Stderr, "gate-run: gate %s (pid %d) is already running on tree %s; waiting for it\n",
		holder.RunID, holder.PID, holder.Tree)
	now := o.Now()
	deadline := now.Add(o.Timeout)
	if started, err := time.Parse(time.RFC3339Nano, holder.StartedAt); err == nil &&
		started.Add(o.Timeout).Before(deadline) {
		deadline = started.Add(o.Timeout)
	}
	for {
		cur, err := readJSON[guard](p.guard)
		if errors.Is(err, os.ErrNotExist) || (err == nil && cur.RunID != holder.RunID) {
			break
		}
		if !o.Now().Before(deadline) {
			fmt.Fprintf(o.Stderr, "gate-run: gave up after %s waiting for gate %s (pid %d); guard file %s\n",
				o.Now().Sub(now).Round(time.Second), holder.RunID, holder.PID, p.guard)
			return ExitHeld, true, nil
		}
		if !o.holderLive(holder) {
			break
		}
		select {
		case <-ctx.Done():
			return 1, false, ctx.Err()
		case sig := <-o.Signals:
			fmt.Fprintf(o.Stderr, "gate-run: interrupted by %v while waiting for gate %s\n", sig, holder.RunID)
			return 128 + signum(sig), true, nil
		case <-time.After(min(o.Poll, max(deadline.Sub(o.Now()), time.Millisecond))):
		}
	}
	res, err := readJSON[result](p.result)
	if err != nil || res.RunID != holder.RunID || res.Tree != holder.Tree {
		// The holder was replaced by a gate on another tree: this tree is no
		// longer current, so this gate-run is superseded too, never a reason to
		// stop the newer gate.
		if cur, err := readJSON[guard](p.guard); err == nil && cur.RunID != holder.RunID &&
			cur.Tree != holder.Tree && o.holderLive(cur) {
			fmt.Fprintf(o.Stderr, "gate-run: tree changed from %s to %s while waiting; gate %s superseded it\n",
				holder.Tree, cur.Tree, cur.RunID)
			return ExitSuperseded, true, nil
		}
		return 0, false, nil
	}
	fmt.Fprintf(o.Stderr, "gate-run: waited for gate %s (pid %d) on tree %s: exit %d\n",
		holder.RunID, holder.PID, holder.Tree, res.ExitCode)
	return res.ExitCode, true, nil
}

// supersede stops a live holder on another tree: it marks the holder
// superseded, stops its process group (SIGTERM, then SIGKILL), verifies the
// group dead and the holder released, and records the holder's run as
// superseded. A holder that cannot be verified stopped is a *HeldError.
func (o *Options) supersede(p paths, holder guard) error {
	if holder.Host != "" && holder.Host != hostname() {
		// Its process group is on another host; this one cannot stop it.
		return &HeldError{PID: holder.PID, Tree: holder.Tree, Worktree: p.worktree}
	}
	if err := writeJSON(p.marker, supersedeMarker{RunID: holder.RunID, Tree: o.Tree, PID: os.Getpid()}); err != nil {
		return err
	}
	fmt.Fprintf(o.Stderr, "gate-run: tree changed from %s to %s; stopping gate %s (pid %d)\n",
		holder.Tree, o.Tree, holder.RunID, holder.PID)
	groups := []int{}
	if holder.PGID > 0 {
		groups = append(groups, holder.PGID)
	}
	released := func() bool {
		cur, err := readJSON[guard](p.guard)
		if errors.Is(err, os.ErrNotExist) || !o.holderLive(holder) {
			return true
		}
		if err == nil && cur.RunID != holder.RunID {
			return true
		}
		// The holder may have started another step since it was read.
		if err == nil && cur.PGID > 0 && !contains(groups, cur.PGID) {
			groups = append(groups, cur.PGID)
		}
		return false
	}
	if !o.stopUntilDead(groups, released, &groups) {
		// Nothing replaced the holder, so it must not void its own result.
		removeMarkerFor(p.marker, holder.RunID)
		return &HeldError{PID: holder.PID, Tree: holder.Tree, Worktree: p.worktree}
	}
	ev := gateledger.Event{
		Event: gateledger.EventFinish, RunID: holder.RunID, Kind: holder.Kind,
		Tree: holder.Tree, Verdict: gateledger.VerdictSuperseded,
		// Full is left to the holder's own start event; RunCount ORs it per run.
	}
	if err := o.Record(ev); err != nil {
		fmt.Fprintf(o.Stderr, "gate-run: the superseded gate %s was not recorded: %v\n", holder.RunID, err)
	}
	return nil
}

// removeMarkerFor removes the supersede marker only while it still names
// runID, so a marker another gate-run wrote meanwhile is kept.
func removeMarkerFor(path, runID string) {
	if m, err := readJSON[supersedeMarker](path); err == nil && m.RunID == runID {
		_ = os.Remove(path)
	}
}

// stopUntilDead sends each group SIGTERM, then SIGKILL after TermGrace, until
// every group is verified dead (ESRCH) and done reports true. live, when
// given, is re-read each round so groups found meanwhile are stopped too. It
// reports false when KillGrace after SIGKILL passes first.
func (o *Options) stopUntilDead(groups []int, done func() bool, live ...*[]int) bool {
	start := time.Now()
	termed, killed := map[int]bool{}, map[int]bool{}
	tick := o.Poll / 20
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	for {
		finished := done()
		if len(live) > 0 {
			groups = *live[0]
		}
		alive := false
		force := time.Since(start) >= o.TermGrace
		for _, pg := range groups {
			if !o.GroupAlive(pg) {
				continue
			}
			alive = true
			if !termed[pg] {
				termed[pg] = true
				_ = o.Stop(pg, false)
			}
			if force && !killed[pg] {
				killed[pg] = true
				_ = o.Stop(pg, true)
			}
		}
		if finished && !alive {
			return true
		}
		if time.Since(start) >= o.TermGrace+o.KillGrace {
			return false
		}
		time.Sleep(tick)
	}
}

// runHeld runs the gate while holding the guard.
func (o *Options) runHeld(ctx context.Context, p paths, g guard) (int, error) {
	full := true
	if !o.RecordsItself {
		ev := gateledger.Event{Event: gateledger.EventStart, RunID: g.RunID, Kind: o.Kind, Tree: o.Tree, Full: full}
		if err := o.Record(ev); err != nil {
			fmt.Fprintf(o.Stderr, "gate-run: this gate's start was not recorded: %v\n", err)
		}
	}
	env := append(os.Environ(), EnvRunID+"="+g.RunID)
	superseded := func() bool {
		m, err := readJSON[supersedeMarker](p.marker)
		return err == nil && m.RunID == g.RunID
	}
	wasSuperseded := false
	sh := &sigHandler{o: o}
	runStep := func(cmd *exec.Cmd) error {
		if superseded() {
			wasSuperseded = true
			return errSuperseded
		}
		cmd.Env = env
		isolate(cmd)
		// The handler reads the group under the same lock, so a signal either
		// sees this step's group or stops it from starting.
		sh.mu.Lock()
		if sh.sig != nil {
			sh.mu.Unlock()
			return errInterrupted
		}
		if err := cmd.Start(); err != nil {
			sh.mu.Unlock()
			return err
		}
		g.PGID = cmd.Process.Pid
		g.PGIDStart, _ = o.ProcStart(g.PGID)
		sh.pgid, sh.pgidStart = g.PGID, g.PGIDStart
		sh.mu.Unlock()
		if err := writeJSON(p.guard, g); err != nil {
			fmt.Fprintf(o.Stderr, "gate-run: the guard was not updated: %v\n", err)
		}
		// A superseder that read the guard before this step started could not
		// see its group; stop it here instead.
		if superseded() {
			_ = o.Stop(g.PGID, true)
		}
		err := cmd.Wait()
		if superseded() {
			wasSuperseded = true
			return errSuperseded
		}
		if sh.interrupted() != nil {
			return errInterrupted
		}
		return err
	}
	quit := make(chan struct{})
	handled := make(chan struct{})
	go sh.watch(quit, handled)
	start := time.Now()
	err := o.Run(ctx, runStep)
	close(quit)
	<-handled
	if sig := sh.interrupted(); sig != nil {
		if !o.RecordsItself {
			ev := gateledger.Event{
				Event: gateledger.EventFinish, RunID: g.RunID, Kind: o.Kind, Tree: o.Tree,
				Verdict: gateledger.VerdictInfra, Full: full, Seconds: int(time.Since(start).Seconds()),
			}
			if rerr := o.Record(ev); rerr != nil {
				fmt.Fprintf(o.Stderr, "gate-run: this gate's result was not recorded: %v\n", rerr)
			}
		}
		// No result file: a waiter must not adopt an interrupted gate's exit.
		if sh.stopped {
			fmt.Fprintf(o.Stderr, "gate-run: interrupted by %v; stopped step group %d\n", sig, sh.pgid)
		} else {
			fmt.Fprintf(o.Stderr, "gate-run: interrupted by %v; step group %d could not be verified stopped\n", sig, sh.pgid)
		}
		return 128 + signum(sig), nil
	}
	if wasSuperseded || superseded() {
		m, _ := readJSON[supersedeMarker](p.marker)
		fmt.Fprintf(o.Stderr, "gate-run: superseded by tree %s; this gate's result is void\n", m.Tree)
		return ExitSuperseded, nil
	}
	code := exitCode(err)
	if err != nil {
		fmt.Fprintln(o.Stderr, err)
	}
	if !o.RecordsItself {
		verdict := gateledger.VerdictPass
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			verdict = gateledger.VerdictFail
		default:
			verdict = gateledger.VerdictInfra
		}
		ev := gateledger.Event{
			Event: gateledger.EventFinish, RunID: g.RunID, Kind: o.Kind, Tree: o.Tree,
			Verdict: verdict, Full: full, Seconds: int(time.Since(start).Seconds()),
		}
		if rerr := o.Record(ev); rerr != nil {
			fmt.Fprintf(o.Stderr, "gate-run: this gate's result was not recorded: %v\n", rerr)
		}
	}
	if werr := writeJSON(p.result, result{RunID: g.RunID, Tree: o.Tree, ExitCode: code}); werr != nil {
		fmt.Fprintf(o.Stderr, "gate-run: the result file was not written: %v\n", werr)
	}
	return code, nil
}

var errSuperseded = errors.New("gate-run: superseded")

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return ee.ExitCode()
	}
	return 1
}

// release removes the guard when it still names runID.
func release(path, runID string) {
	if g, err := readJSON[guard](path); err == nil && g.RunID == runID {
		_ = os.Remove(path)
	}
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func newID() (string, error) {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

func readJSON[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("gaterun: %s: %w", path, err)
	}
	return v, nil
}

func writeTemp(path string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("gaterun: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("gaterun: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("gaterun: %w", err)
	}
	return f.Name(), nil
}

// writeJSON replaces path atomically.
func writeJSON(path string, v any) error {
	tmp, err := writeTemp(path, v)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("gaterun: %w", err)
	}
	return nil
}

var errInterrupted = errors.New("gate-run: interrupted")

// sigHandler stops the running step's group when gate-run is signalled.
type sigHandler struct {
	o         *Options
	mu        sync.Mutex
	sig       os.Signal
	pgid      int
	pgidStart int64
	stopped   bool
}

func (h *sigHandler) interrupted() os.Signal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sig
}

// watch waits for a signal until quit closes. On one it refuses further
// steps, forwards the signal to the current step's group, and SIGKILLs the
// group after TermGrace (at once on a second signal) until it is verified
// dead. handled closes when it is done.
func (h *sigHandler) watch(quit <-chan struct{}, handled chan<- struct{}) {
	defer close(handled)
	var sig os.Signal
	select {
	case <-quit:
		return
	case sig = <-h.o.Signals:
	}
	h.mu.Lock()
	h.sig = sig
	pgid, start := h.pgid, h.pgidStart
	h.mu.Unlock()
	if !h.o.ownGroup(pgid, start) {
		h.stopped = true
		return
	}
	_ = signalGroup(pgid, sig)
	tick := max(h.o.Poll/20, 10*time.Millisecond)
	graceEnd := time.Now().Add(h.o.TermGrace)
	for time.Now().Before(graceEnd) && h.o.GroupAlive(pgid) {
		select {
		case <-h.o.Signals:
			graceEnd = time.Now()
		case <-time.After(tick):
		}
	}
	killEnd := time.Now().Add(h.o.KillGrace)
	for h.o.GroupAlive(pgid) {
		if !time.Now().Before(killEnd) {
			return
		}
		_ = h.o.Stop(pgid, true)
		time.Sleep(tick)
	}
	h.stopped = true
}

// signum is a signal's number, for the conventional 128+signo exit.
func signum(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return int(s)
	}
	return 1
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
