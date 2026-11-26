//go:build opencode_integration && unix

package adapters

// The adversarial config-merge and plugin-loading suite (ADR-022 § 8, #1632).
// A target repository, and a stage writing into it, can supply opencode.json
// and .opencode/**. Each test below pins what the pinned opencode binary does
// with one such fixture, so an upgrade that changes merge or plugin semantics
// turns this suite red instead of silently opening a hole:
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -count=1 -v \
//	  -run 'TestOpenCode(PurePluginLoading|InlineDenyBeatsProjectAllow|ArrayMerge|ProviderBaseURLPrecedence|ConfigDiscoveryAboveWorktree|DisableProjectConfig|MergeHarnessReapsEveryProcess|MergeHarnessReclaimsOrphanedLocks)$'
//
// The fixtures and the observed answers are in testdata/opencode-adversarial/.
// Every opencode process runs with a throwaway HOME, TMPDIR and per-run root
// under t.TempDir(), the isolation environment OpenCodeIsolationEnv builds
// (#1616), and an environment built from nothing, so neither the operator's
// config nor an inherited OPENCODE_* variable takes part. Nothing leaves the
// machine: a run either names a provider OpenCode does not know, and exits on
// the model lookup, or names a provider whose baseURL is a loopback stub the
// test owns, which refuses every request. OpenCode installs plugin
// dependencies with npm, so every spawn is pointed at a loopback npm registry
// stub as well. Each process runs in its own process group under a 60-second
// cap, and cleanup fails the test if any of them, or anything in its group,
// is still alive. A process that reaches the cap is described before it is
// killed, and before every spawn the harness frees any OpenCode lock an
// earlier, reaped process left behind (#2454).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// mcVersion is the opencode version every answer here was observed on. A
// different binary fails rather than skips: its behaviour is unverified.
//
// Moved from 2.2.7 to 2.3.2 (#3608). Config discovery above the worktree and
// OPENCODE_DISABLE_PROJECT_CONFIG=1 behave as on 2.2.7, so Q5/Q6 are unchanged.
// What changed: `debug ... --standalone` now starts a file watcher over every
// directory up to /, slow enough to pass mcTimeout, so env() turns it off.
const mcVersion = "2.3.2"

// mcTimeout caps every opencode process the suite starts.
const mcTimeout = 60 * time.Second

// mcFixtures holds one fixture tree per question.
const mcFixtures = "testdata/opencode-adversarial"

// mcUnknownModel names a provider OpenCode does not know, so a run exits 1 on
// the model lookup, about a second in, before any model request.
const mcUnknownModel = "nosuchprovider/x"

// mcStubModel is the provider/model a run names when the test needs to see
// what OpenCode would send to a model: its baseURL is the test's model stub.
const mcStubModel = "fixture-endpoint/fixture-model"

// mcSentinelEnv names the file every fixture plugin appends its name to.
const mcSentinelEnv = "ADVERSARIAL_FIXTURE_SENTINEL"

// mcProjectDir is the placeholder a JSON fixture uses for the absolute path of
// its project copy.
const mcProjectDir = "@PROJECT_DIR@"

// mcRemoteURL is the placeholder the remote-instructions fixture uses for the
// loopback stub's URL, and mcRemoteInstructionPath the path it names there.
const (
	mcRemoteURL             = "@REMOTE_URL@"
	mcRemoteInstructionPath = "/repo-remote-instruction.md"
)

// mcPluginSDKPath is the registry request for the package OpenCode installs
// into a config directory before it loads a plugin.
const mcPluginSDKPath = "/@opencode-ai%2fplugin"

// mcNpmPluginPackage is the npm plugin q1's fixture project loads, and
// mcNpmPluginPlaceholder the token its committed opencode.json names instead
// of the real spec. The harness's npm registry stub always serves this
// package name on loopback, but the placeholder is not itself a syntactically
// valid npm package name (uppercase, underscores), so it can never resolve
// against the real registry: a contributor who runs `opencode` by hand
// straight from testdata/ (skipping copyFixture's rewrite, and so the real
// npm registry, not the stub) finds no such package instead of depending on
// "adversarial-fixture-npm-plugin" staying unclaimed there forever (#1742).
const (
	mcNpmPluginPackage     = "adversarial-fixture-npm-plugin"
	mcNpmPluginPlaceholder = "@ADVERSARIAL_NPM_PLUGIN_SPEC@"
)

// mcPrompt is the user message every run sends on stdin.
const mcPrompt = "adversarial fixture prompt"

// mcHarness is one isolated OpenCode installation: its own HOME, TMPDIR,
// per-run root and npm registry stub. A fresh harness starts with an empty
// data directory and an empty plugin cache.
type mcHarness struct {
	t        *testing.T
	bin      string
	home     string
	root     string
	tmp      string
	scratch  string
	registry *mcRegistryStub
	pids     *mcPIDLedger
	timeout  time.Duration
}

// newMCHarness resolves the opencode binary, checks its version and returns a
// harness whose processes are reaped and verified dead at cleanup. packages
// maps an npm package name to the fixture directory the registry stub serves
// for it; every other package name gets a 404.
func newMCHarness(t *testing.T, packages map[string]string) *mcHarness {
	t.Helper()
	bin, err := exec.LookPath("opencode")
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("opencode is not on PATH, and CI must run this suite: install opencode-ai@%s", mcVersion)
		}
		t.Skipf("opencode is not on PATH; install opencode-ai@%s to run the adversarial suite", mcVersion)
	}
	// Directories first, stubs next, the ledger last: cleanups run last in,
	// first out, so every process is reaped before a stub closes or a
	// directory is removed.
	base := mcResolved(t, t.TempDir())
	h := &mcHarness{
		t:       t,
		bin:     bin,
		home:    filepath.Join(base, "home"),
		root:    filepath.Join(base, "run-root"),
		tmp:     filepath.Join(base, "tmp"),
		scratch: filepath.Join(base, "scratch"),
		timeout: mcTimeout,
	}
	for _, dir := range []string{h.home, h.tmp, h.scratch} {
		mcMkdir(t, dir)
	}
	for _, x := range openCodeXDGDirs {
		mcMkdir(t, filepath.Join(h.root, x.dir))
	}
	h.registry = newMCRegistryStub(t, packages)
	h.pids = newMCPIDLedger(t)
	res := h.run(h.scratch, nil, "", "--version")
	if res.exit != 0 {
		t.Fatalf("opencode --version exited %d:\n%s", res.exit, res.stderr)
	}
	if v, _ := ParseOpenCodeVersion(res.stdout); v != mcVersion {
		t.Fatalf("opencode %s is installed; this suite was observed on %s. Re-run it against the new version and re-record ADR-022's results table before changing the pin", v, mcVersion)
	}
	return h
}

// env is the complete environment of a spawn: OpenCodeIsolationEnv's
// variables for this harness's root, the switches that keep a run off the
// network, and extra. Nothing is inherited from the test process.
func (h *mcHarness) env(extra map[string]string) []string {
	h.t.Helper()
	iso, err := OpenCodeIsolationEnv(OpenCodeIsolation{
		Root:             h.root,
		Home:             h.home,
		Lookup:           envLookup(map[string]string{"HOME": h.home}),
		GOOS:             runtime.GOOS,
		MachineConfigDir: filepath.Join(h.home, ".nightgauge"),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	// OPENCODE_DISABLE_PROJECT_CONFIG is not in OpenCodeIsolationEnv's output
	// today, but is tracked to join it (#1626/#1638). Once it does, copying
	// iso wholesale into every spawn would flip every positive control this
	// suite has — Q1's project-config-on control, Q2/Q4/Q5's project-alone
	// controls, Q3's concat assertions, Q6's without-the-switch control — off
	// silently, with nothing about pinned OpenCode behavior having changed.
	// Drop it here so only a caller that opts in (Q6, via extra) sets it.
	delete(iso, "OPENCODE_DISABLE_PROJECT_CONFIG")
	env := map[string]string{
		"HOME":   h.home,
		"PATH":   "/usr/bin:/bin",
		"TMPDIR": h.tmp,
		// OpenCode installs plugin dependencies through npm, which reads its
		// registry from this variable; the stub answers on loopback.
		"npm_config_registry": h.registry.URL + "/",
		// Also set by OpenCodeIsolationEnv; restated so the suite's own
		// guarantee does not rest on that list.
		"OPENCODE_DISABLE_MODELS_FETCH": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":   "1",
		"OPENCODE_DISABLE_SHARE":        "1",
		// 2.3.2's `debug ... --standalone` starts a `serve --stdio` child
		// whose file watcher subscribes to the project and every directory
		// above it up to /. Observed (--print-logs): about 10s for one
		// `debug config` from a TempDir, and past mcTimeout once the suite's
		// parallel tests run together, so Q5/Q6 and the lock test were
		// killed mid-listing. No question here reads a file change; with
		// the watcher off the same call takes about 0.35s (#3608).
		"OPENCODE_DISABLE_FILEWATCHER": "1",
	}
	maps.Copy(env, iso)
	maps.Copy(env, extra)
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// mcResult is one finished opencode process.
type mcResult struct {
	stdout, stderr string
	exit           int
	timedOut       bool
}

// mcProc is a started opencode process.
type mcProc struct {
	cmd      *exec.Cmd
	started  time.Time
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	finished chan struct{}
	result   mcResult
	// diagnosis is what the harness saw at the cap, taken before the kill.
	diagnosis string
}

// start spawns opencode in dir, in its own process group, under the
// harness's cap, and records its PID with the ledger before anything else.
func (h *mcHarness) start(dir string, extra map[string]string, stdin string, args ...string) *mcProc {
	h.t.Helper()
	h.reclaimOrphanedLocks()
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Dir = dir
	cmd.Env = h.env(extra)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	p := &mcProc{cmd: cmd, started: time.Now(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, finished: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On the cap, the whole group goes, not only the leader. What the group
	// was doing is recorded first, while it is still there to look at: Wait
	// returns only after Cancel does, so run reads it race-free.
	cmd.Cancel = func() error {
		p.diagnosis = h.diagnose(cmd.Process.Pid, p.started)
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		h.t.Fatalf("start opencode %s: %v", strings.Join(args, " "), err)
	}
	h.pids.add(cmd.Process.Pid)
	go func() {
		defer close(p.finished)
		defer cancel()
		err := cmd.Wait()
		// Mark the PID finished, and check its group for a survivor, the
		// instant Wait returns: reap runs tens of seconds later, on cleanup,
		// and the kernel is free to recycle a genuinely empty group's ID in
		// that gap (#1742).
		h.pids.finished(cmd.Process.Pid)
		p.result = mcResult{stdout: p.stdout.String(), stderr: p.stderr.String(), timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
		var exitErr *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exitErr):
			p.result.exit = exitErr.ExitCode()
		default:
			p.result.exit = -1
		}
	}()
	return p
}

// wait returns the finished process's result.
func (p *mcProc) wait() mcResult {
	<-p.finished
	return p.result
}

// run spawns opencode and waits for it. A process that reaches the cap fails
// the test: every command here finishes in seconds.
func (h *mcHarness) run(dir string, extra map[string]string, stdin string, args ...string) mcResult {
	h.t.Helper()
	p := h.start(dir, extra, stdin, args...)
	res := p.wait()
	if res.timedOut {
		h.t.Fatalf("opencode %s ran past the %s cap and its process group was killed\nstdout:\n%s\nstderr:\n%s\n%s", strings.Join(args, " "), h.timeout, res.stdout, res.stderr, p.diagnosis)
	}
	return res
}

// locksDir is where OpenCode keeps its cross-process locks, under the run's
// XDG_STATE_HOME.
func (h *mcHarness) locksDir() string {
	return filepath.Join(h.root, "state", "opencode", "locks")
}

// reclaimOrphanedLocks removes every OpenCode lock directory in the harness
// whose owner has exited (#2454). It runs before every spawn, when no process
// of this harness is running, and the state directory is the harness's own,
// so a dead owner can only be an opencode this harness started and reaped.
//
// OpenCode 1.18.30 serialises npm installs across processes with a lock
// directory, XDG_STATE_HOME/opencode/locks/<sha1 of "npm-install:<dir>">.lock,
// whose meta.json names the owning pid and whose heartbeat file the owner
// touches every 20 seconds. Every config load starts a detached background
// install of @opencode-ai/plugin into each config directory under that lock,
// and a process that exits before the install finishes never releases it:
// under load that is routine, for a `debug` command that has printed its
// answer as much as for a run that ended on the model error. OpenCode breaks
// such a lock only once its heartbeat is a minute old. A later process in the
// same harness whose config names a plugin waits for that install, so it
// waits out the rest of that minute first, silently: a `debug config` that
// takes three seconds took 64 against an orphaned lock, past the cap with
// empty stdout and stderr. The harness knows the owner is dead, because it
// reaped it, so it frees the lock at once instead of making every later call
// race OpenCode's minute against its own cap. A lock whose owner is alive is
// left alone.
func (h *mcHarness) reclaimOrphanedLocks() {
	h.t.Helper()
	dir := h.locksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var keys map[string]string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".lock")
		if !ok || !e.IsDir() {
			continue
		}
		lock := filepath.Join(dir, e.Name())
		var meta struct {
			PID int `json:"pid"`
		}
		// No readable meta.json is a lock still being taken, or one OpenCode's
		// own breaker is replacing: not the harness's to touch.
		raw, err := os.ReadFile(filepath.Join(lock, "meta.json"))
		if err != nil || json.Unmarshal(raw, &meta) != nil || meta.PID <= 0 || mcSignalable(meta.PID) {
			continue
		}
		if err := os.RemoveAll(lock); err != nil {
			h.t.Fatalf("reclaim the lock opencode pid %d left behind: %v", meta.PID, err)
		}
		if keys == nil {
			keys = h.lockKeys()
		}
		key, known := keys[name]
		if !known {
			key = e.Name()
		}
		h.t.Logf("reclaimed OpenCode lock %s, which opencode pid %d left behind when it exited (#2454)", key, meta.PID)
	}
}

// diagnose describes a process group that reached the cap, before the kill:
// its processes, their sockets and open files, OpenCode's cross-process lock
// directories in the harness's state directory, the npm registry stub's
// requests since the process started, and the tail of OpenCode's own log, so
// a stall is diagnosable from the test output alone (#2454).
func (h *mcHarness) diagnose(pid int, started time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "At the cap, %s after start (pid %d):\n", time.Since(started).Round(time.Millisecond), pid)
	group := mcProcessTree(pid)
	b.WriteString("process group and descendants (pid ppid pgid stat elapsed cpu command):\n")
	pids := make([]string, 0, len(group))
	for _, row := range group {
		fmt.Fprintf(&b, "  %s\n", mcClip(row.line, 300))
		if row.pid != "" {
			pids = append(pids, row.pid)
		}
	}
	if len(pids) > 0 {
		b.WriteString("open sockets and files (lsof, mapped files omitted):\n")
		for _, line := range mcLsof(strings.Join(pids, ",")) {
			fmt.Fprintf(&b, "  %s\n", mcClip(line, 300))
		}
	}
	b.WriteString("OpenCode locks (XDG_STATE_HOME/opencode/locks):\n")
	for _, line := range h.locks() {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	b.WriteString("npm registry stub requests since start:\n")
	for _, line := range h.registry.requestsSince(started) {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	b.WriteString(h.logTail(40))
	return b.String()
}

// mcProcessRow is one line of ps output.
type mcProcessRow struct{ pid, line string }

// mcProcessTree lists every process in the process group leader leads, and
// every descendant of the leader that has left that group.
func mcProcessTree(leader int) []mcProcessRow {
	out, err := mcCommand("ps", "-A", "-ww", "-o", "pid=,ppid=,pgid=,stat=,etime=,time=,command=")
	if err != nil {
		return []mcProcessRow{{line: "ps failed: " + err.Error()}}
	}
	type proc struct{ pid, ppid, pgid, line string }
	var procs []proc
	parent := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			procs = append(procs, proc{f[0], f[1], f[2], strings.TrimSpace(line)})
			parent[f[0]] = f[1]
		}
	}
	id := fmt.Sprint(leader)
	descends := func(pid string) bool {
		for depth := 0; pid != "" && depth < 64; depth++ {
			if pid == id {
				return true
			}
			pid = parent[pid]
		}
		return false
	}
	var rows []mcProcessRow
	for _, p := range procs {
		if p.pgid == id || descends(p.pid) {
			rows = append(rows, mcProcessRow{pid: p.pid, line: p.line})
		}
	}
	return rows
}

// mcLsof lists pids' open descriptors, without the memory-mapped files and
// the binary itself, which every bun process has and which say nothing.
func mcLsof(pids string) []string {
	out, err := mcCommand("lsof", "-n", "-P", "-w", "-p", pids)
	if err != nil && out == "" {
		return []string{"lsof failed: " + err.Error()}
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] == "txt" || f[3] == "mem" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > 80 {
		lines = append(lines[:80], fmt.Sprintf("... %d more", len(lines)-80))
	}
	return lines
}

// mcCommand runs a diagnostic command under a short cap of its own: a
// diagnosis must never be what holds a failing test up.
func mcCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func mcClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// locks describes every lock directory OpenCode's cross-process lock
// (EffectFlock, under XDG_STATE_HOME/opencode/locks) holds in this harness:
// the key it guards when that is a key the harness can name, the owner pid
// from its meta.json and whether that pid is still alive, and how long ago
// the owner last touched its heartbeat. OpenCode treats a lock as stale only
// once its heartbeat is a minute old.
func (h *mcHarness) locks() []string {
	dir := h.locksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	keys := h.lockKeys()
	var out []string
	for _, e := range entries {
		line := e.Name()
		if key, ok := keys[strings.TrimSuffix(e.Name(), ".lock")]; ok {
			line += " key=" + key
		}
		var meta struct {
			PID       int    `json:"pid"`
			CreatedAt string `json:"createdAt"`
		}
		if raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "meta.json")); err == nil && json.Unmarshal(raw, &meta) == nil {
			line += fmt.Sprintf(" owner=%d alive=%v created=%s", meta.PID, mcSignalable(meta.PID), meta.CreatedAt)
		}
		if info, err := os.Stat(filepath.Join(dir, e.Name(), "heartbeat")); err == nil {
			if age := time.Since(info.ModTime()).Round(time.Millisecond); age >= 0 {
				line += fmt.Sprintf(" heartbeat=%s ago", age)
			} else {
				line += fmt.Sprintf(" heartbeat=%s ahead", -age)
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		out = append(out, "(none)")
	}
	return out
}

// lockKeys maps the lock directory name of every npm-install lock the harness
// can predict, OpenCode's sha1 of "npm-install:<dir>", back to its key.
func (h *mcHarness) lockKeys() map[string]string {
	dirs := []string{filepath.Join(h.root, "config", "opencode")}
	pkgs, _ := filepath.Glob(filepath.Join(h.root, "cache", "opencode", "packages", "*"))
	dirs = append(dirs, pkgs...)
	nested, _ := filepath.Glob(filepath.Join(h.scratch, "*", ".opencode"))
	dirs = append(dirs, nested...)
	keys := map[string]string{}
	for _, d := range dirs {
		key := "npm-install:" + d
		sum := sha1.Sum([]byte(key))
		keys[hex.EncodeToString(sum[:])] = key
	}
	return keys
}

// logTail returns the last n lines of every OpenCode log file in the harness.
func (h *mcHarness) logTail(n int) string {
	files, _ := filepath.Glob(filepath.Join(h.root, "data", "opencode", "log", "*"))
	if len(files) == 0 {
		return "OpenCode log: none written\n"
	}
	var b strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(&b, "OpenCode log %s: %v\n", f, err)
			continue
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		fmt.Fprintf(&b, "OpenCode log %s, last %d lines:\n", f, len(lines))
		for _, line := range lines {
			fmt.Fprintf(&b, "  %s\n", mcClip(line, 400))
		}
	}
	return b.String()
}

// mcPIDLedger records every opencode PID a test starts. At cleanup it kills
// each one's process group and fails the test if a PID, or any member of its
// group, still answers kill -0 after five seconds.
type mcPIDLedger struct {
	mu   sync.Mutex
	pids []int
	// finishedEmpty holds true for a pid whose process had already exited,
	// with nothing left in its process group, the instant finished() checked
	// it (see start's goroutine). reap must not SIGKILL -pid for such an
	// entry: once a group is genuinely empty the kernel may reuse its id for
	// an unrelated process group, so a kill issued tens of seconds later, at
	// cleanup, could hit a same-user group that has nothing to do with this
	// test (#1742).
	finishedEmpty map[int]bool
}

func newMCPIDLedger(t *testing.T) *mcPIDLedger {
	l := &mcPIDLedger{finishedEmpty: map[int]bool{}}
	t.Cleanup(func() { l.reap(t) })
	return l
}

func (l *mcPIDLedger) add(pid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pids = append(l.pids, pid)
}

// finished records that pid's process has been Wait()ed, and whether its
// process group already had no member left to signal at that instant.
func (l *mcPIDLedger) finished(pid int) {
	empty := !mcSignalable(-pid)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finishedEmpty[pid] = empty
}

func (l *mcPIDLedger) recorded() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.pids)
}

// killable is the recorded PIDs reap should send -pid SIGKILL to: every one
// not already known to have finished with an empty group.
func (l *mcPIDLedger) killable() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int
	for _, pid := range l.pids {
		if !l.finishedEmpty[pid] {
			out = append(out, pid)
		}
	}
	return out
}

// survivors returns every recorded PID that is still alive, or whose process
// group still has a member.
func (l *mcPIDLedger) survivors() []int {
	var alive []int
	for _, pid := range l.recorded() {
		if mcSignalable(pid) || mcSignalable(-pid) {
			alive = append(alive, pid)
		}
	}
	return alive
}

// reap kills every recorded process group not already known to have finished
// empty, and waits for all recorded PIDs to be gone.
func (l *mcPIDLedger) reap(t testing.TB) {
	for _, pid := range l.killable() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive := l.survivors()
		if len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("opencode processes survived cleanup (kill -0 still succeeds for these PIDs or their process groups): %v", alive)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// mcSignalable is kill -0: a negative id asks about a process group. EPERM
// means the process exists and belongs to someone else, which still counts.
func mcSignalable(id int) bool {
	err := syscall.Kill(id, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// mcRegistryStub is a loopback npm registry. It serves the fixture packages
// it was given, answers 404 for every other name, and records every request.
type mcRegistryStub struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	times []time.Time
}

func newMCRegistryStub(t *testing.T, packages map[string]string) *mcRegistryStub {
	t.Helper()
	type pkg struct{ doc, tarball []byte }
	served := map[string]*pkg{}
	s := &mcRegistryStub{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		s.mu.Lock()
		s.paths = append(s.paths, path)
		s.times = append(s.times, time.Now())
		s.mu.Unlock()
		for name, p := range served {
			switch path {
			case "/" + name:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(p.doc)
				return
			case "/" + name + "/-/" + name + "-1.0.0.tgz":
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(p.tarball)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	s.Start()
	t.Cleanup(s.Close)
	for name, dir := range packages {
		tarball := mcPackTarball(t, dir)
		sum512 := sha512.Sum512(tarball)
		sum1 := sha1.Sum(tarball)
		doc, err := json.Marshal(map[string]any{
			"name":      name,
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"versions": map[string]any{"1.0.0": map[string]any{
				"name": name, "version": "1.0.0", "type": "module", "main": "index.js",
				"dist": map[string]string{
					"tarball":   fmt.Sprintf("%s/%s/-/%s-1.0.0.tgz", s.URL, name, name),
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum512[:]),
					"shasum":    hex.EncodeToString(sum1[:]),
				},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		served[name] = &pkg{doc: doc, tarball: tarball}
	}
	return s
}

// requests returns the request paths the stub has seen.
func (s *mcRegistryStub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

// requestsSince returns the requests the stub has seen since start, each with
// its offset from start.
func (s *mcRegistryStub) requestsSince(start time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for i, at := range s.times {
		if !at.Before(start) {
			out = append(out, fmt.Sprintf("+%s %s", at.Sub(start).Round(time.Millisecond), s.paths[i]))
		}
	}
	return out
}

// mcPackTarball packs dir the way npm publishes a package: every file under
// a package/ prefix, gzipped.
func mcPackTarball(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: "package/" + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(raw)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		t.Fatalf("pack %s: %v", dir, err)
	}
	return buf.Bytes()
}

// mcModelStub stands in for an OpenAI-compatible model server on loopback. It
// records every request and refuses it with a 400, so a run that reaches it
// ends after one request. With a hold condition it answers only once the
// condition holds, or after ten seconds, which keeps a run alive long enough
// for its background work to be observed.
type mcModelStub struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []mcModelRequest
}

type mcModelRequest struct {
	path   string
	header http.Header
	body   string
}

func newMCModelStub(t *testing.T, hold func() bool) *mcModelStub {
	t.Helper()
	s := &mcModelStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for deadline := time.Now().Add(10 * time.Second); hold != nil && !hold() && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, mcModelRequest{path: r.URL.Path, header: r.Header.Clone(), body: string(body)})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"the adversarial model stub refuses every request","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mcModelStub) requests() []mcModelRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// bodies joins every request body the stub received.
func (s *mcModelStub) bodies() string {
	var b strings.Builder
	for _, r := range s.requests() {
		b.WriteString(r.body)
		b.WriteByte('\n')
	}
	return b.String()
}

// withModelStub returns inline with the stub provider added: a provider block
// whose baseURL is the stub, narrowed to that provider, and no title request.
func withModelStub(t *testing.T, inline string, stub *mcModelStub) string {
	t.Helper()
	cfg := map[string]any{}
	if inline != "" {
		if err := json.Unmarshal([]byte(inline), &cfg); err != nil {
			t.Fatalf("inline fixture is not JSON: %v", err)
		}
	}
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	block, _ := providers["fixture-endpoint"].(map[string]any)
	if block == nil {
		block = map[string]any{
			"npm":    "@ai-sdk/openai-compatible",
			"name":   "Fixture endpoint",
			"models": map[string]any{"fixture-model": map[string]any{"id": "fixture-model"}},
		}
	}
	options, _ := block["options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
	}
	options["baseURL"] = stub.URL + "/v1"
	block["options"] = options
	providers["fixture-endpoint"] = block
	cfg["provider"] = providers
	cfg["enabled_providers"] = []string{"fixture-endpoint"}
	cfg["agent"] = map[string]any{"title": map[string]any{"disable": true}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// project copies a fixture tree to a fresh directory, git-inits it and
// returns its resolved path. A file named <name>.fixture.md is written as
// <name>.md (the tree holds no instruction or skill file under its real
// name, so no tool reading this repository takes one for its own), and
// @PROJECT_DIR@ in a JSON file becomes the copy's path.
//
// The git init matches production, where a stage's cwd is always a git
// worktree root: without it, OpenCode's directory discovery can walk up past
// the copy toward wherever TMPDIR resolves, and on a TMPDIR under the
// operator's real home that loads the operator's own .opencode config or
// plugins as "project config" (#1742).
func (h *mcHarness) project(fixture string) string {
	h.t.Helper()
	dst := filepath.Join(h.scratch, "project-"+strings.ReplaceAll(fixture, "/", "-"))
	h.copyFixture(fixture, dst, dst)
	h.git(dst, "init", "-q")
	return dst
}

// copyFixture copies the fixture tree src into dst, substituting projectDir
// for @PROJECT_DIR@ and the real npm plugin spec for mcNpmPluginPlaceholder.
func (h *mcHarness) copyFixture(src, dst, projectDir string) {
	h.t.Helper()
	root := filepath.Join(mcFixtures, src)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.Ext(path) == ".json" {
			raw = []byte(strings.ReplaceAll(string(raw), mcProjectDir, projectDir))
			raw = []byte(strings.ReplaceAll(string(raw), mcNpmPluginPlaceholder, mcNpmPluginPackage+"@1.0.0"))
		}
		name := strings.Replace(filepath.Base(rel), ".fixture.md", ".md", 1)
		return os.WriteFile(filepath.Join(dst, filepath.Dir(rel), name), raw, 0o644)
	})
	if err != nil {
		h.t.Fatalf("copy fixture %s: %v", src, err)
	}
}

// inline reads an inline-config fixture (OPENCODE_CONFIG_CONTENT) for the
// project at projectDir.
func (h *mcHarness) inline(fixture, projectDir string) string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(mcFixtures, fixture))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), mcProjectDir, projectDir)
}

// withInline returns extra with OPENCODE_CONFIG_CONTENT set to inline, when
// there is one.
func withInline(inline string, extra map[string]string) map[string]string {
	out := maps.Clone(extra)
	if out == nil {
		out = map[string]string{}
	}
	if inline != "" {
		out["OPENCODE_CONFIG_CONTENT"] = inline
	}
	return out
}

// debugJSON runs an `opencode debug` subcommand in dir and decodes its JSON.
func (h *mcHarness) debugJSON(dir string, extra map[string]string, out any, args ...string) {
	h.t.Helper()
	res := h.run(dir, extra, "", append(append([]string{"debug"}, args...), "--standalone")...)
	if res.exit != 0 {
		h.t.Fatalf("opencode debug %s exited %d:\n%s\n%s", strings.Join(args, " "), res.exit, res.stdout, res.stderr)
	}
	if err := json.Unmarshal([]byte(res.stdout), out); err != nil {
		h.t.Fatalf("opencode debug %s printed no JSON: %v\n%s", strings.Join(args, " "), err, res.stdout)
	}
}

// mcSource is one configuration source `opencode debug config --standalone`
// lists on 2.x: a config directory (info null) or a document, with the path
// it was read from (null for OPENCODE_CONFIG_CONTENT) and its own keys as
// 2.x reads them, 1.x keys already translated. 2.x prints no merged config.
type mcSource struct {
	Type string         `json:"type"`
	Path *string        `json:"path"`
	Info map[string]any `json:"info"`
}

// debugConfig is `opencode debug config --standalone` in dir: the sources.
func (h *mcHarness) debugConfig(dir, inline string, extra map[string]string) []mcSource {
	h.t.Helper()
	var sources []mcSource
	h.debugJSON(dir, withInline(inline, extra), &sources, "config")
	return sources
}

// mcSourcePaths is the path of every source with one, in order, under root
// (relative to it) when root is set; sources outside root are left out.
func mcSourcePaths(sources []mcSource, root string) []string {
	var out []string
	for _, src := range sources {
		if src.Path == nil {
			continue
		}
		p := *src.Path
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		if root != "" {
			rel, err := filepath.Rel(root, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			p = rel
		}
		out = append(out, src.Type+":"+p)
	}
	return out
}

// mcInlineDocument is the OPENCODE_CONFIG_CONTENT source's keys: the document
// with no path. nil when there is none.
func mcInlineDocument(sources []mcSource) map[string]any {
	for _, src := range sources {
		if src.Type == "document" && src.Path == nil {
			return src.Info
		}
	}
	return nil
}

// mcAgents is `opencode debug agents --standalone` in dir: every resolved
// agent, by id.
func (h *mcHarness) mcAgents(dir, inline string, extra map[string]string) map[string]map[string]any {
	h.t.Helper()
	var list []map[string]any
	h.debugJSON(dir, withInline(inline, extra), &list, "agents")
	out := map[string]map[string]any{}
	for _, a := range list {
		out[fmt.Sprint(a["id"])] = a
	}
	return out
}

// mcBuiltinAgents are the agents 2.3.2 defines itself (`debug agents
// --standalone` with no config); the list is the same as 2.2.7's.
var mcBuiltinAgents = []string{"build", "compaction", "explore", "general", "plan", "summary", "title"}

// mcFixtureAgents is the id of every resolved agent that is not built in,
// sorted.
func (h *mcHarness) mcFixtureAgents(dir, inline string, extra map[string]string) []string {
	h.t.Helper()
	var ids []string
	for id := range h.mcAgents(dir, inline, extra) {
		if !slices.Contains(mcBuiltinAgents, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// mcAgentRules returns an agent's resolved rules for action, in order, as
// "resource=effect".
func mcAgentRules(agent map[string]any, action string) []string {
	var rules []string
	list, _ := agent["permissions"].([]any)
	for _, r := range list {
		rule, _ := r.(map[string]any)
		if rule["action"] == action {
			rules = append(rules, fmt.Sprintf("%v=%v", rule["resource"], rule["effect"]))
		}
	}
	return rules
}

// mcRunNeedsDispatch skips a question whose observation needs a 2.x
// `opencode run` that loads the per-run config and its plugin: 2.x runs a
// prompt against its shared background service unless --standalone is
// passed, which dispatch passes since #3303, and loads a plugin only from a
// directory, which #3302 provides. Each such question names what it waits on.
func mcRunNeedsDispatch(t *testing.T, what string) {
	t.Helper()
	t.Skipf("on opencode %s, %s needs a 2.x `opencode run` (--standalone, #3303; a directory plugin, #3302)", mcVersion, what)
}

// runUnknownModel runs `opencode run` on a provider OpenCode does not know,
// with the fixture plugins' sentinel inside the harness, and returns what the
// plugins wrote. The run must end on the model lookup: anything else means
// the harness did not observe what it claims to.
func (h *mcHarness) runUnknownModel(dir, inline string, extra map[string]string, flags ...string) []string {
	h.t.Helper()
	sentinel := filepath.Join(h.scratch, fmt.Sprintf("sentinel-%d", time.Now().UnixNano()))
	env := withInline(inline, extra)
	env[mcSentinelEnv] = sentinel
	args := append([]string{"run"}, flags...)
	args = append(args, "--format", "json", "--print-logs", "--log-level", "error", "-m", mcUnknownModel)
	res := h.run(dir, env, mcPrompt, args...)
	if res.exit != 1 || !strings.Contains(res.stderr, "Model not found: "+mcUnknownModel) {
		h.t.Fatalf("the run did not end on the unknown model (exit %d):\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	return mcSentinelLines(h.t, sentinel)
}

// runStubModel runs `opencode run` on the model stub and returns the run's
// result and what the fixture plugins wrote. The run must reach the stub.
func (h *mcHarness) runStubModel(dir, inline string, stub *mcModelStub, extra map[string]string) (mcResult, []string) {
	h.t.Helper()
	sentinel := filepath.Join(h.scratch, fmt.Sprintf("sentinel-%d", time.Now().UnixNano()))
	env := withInline(withModelStub(h.t, inline, stub), extra)
	env[mcSentinelEnv] = sentinel
	before := len(stub.requests())
	res := h.run(dir, env, mcPrompt, "run", "--standalone", "--format", "json", "--print-logs", "--log-level", "error", "-m", mcStubModel)
	if len(stub.requests()) == before {
		h.t.Fatalf("the run never reached the model stub (exit %d):\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	return res, mcSentinelLines(h.t, sentinel)
}

func mcSentinelLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	slices.Sort(lines)
	return lines
}

func mcResolved(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func mcMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mcKeys(v any) []string {
	m, _ := v.(map[string]any)
	return slices.Sorted(maps.Keys(m))
}

func mcStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// mcDig walks nested objects by key.
func mcDig(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

// TestOpenCodePurePluginLoading pins question 1: --pure skips both a project
// .opencode/plugins/*.ts file and a project plugin[] npm entry; the npm plugin
// is never even requested from the registry. The positive control runs first,
// in a fresh harness: without --pure both fixture plugins must write the
// sentinel, or the harness is invalid and silence under --pure proves nothing.
//
// It also pins a request that is not a plugin load. Read from the bundled
// source: for every config directory it loads, the run's own XDG config
// directory always among them, OpenCode starts a background npm install of
// @opencode-ai/plugin, and only a run that loads a plugin waits for it. So a
// run with no plugin at all asks the registry too, once it lives long enough;
// one that ends on the model lookup, about a second in, may exit first, which
// is why the --pure run below may or may not show that request.
func TestOpenCodePurePluginLoading(t *testing.T) {
	t.Parallel()
	mcRunNeedsDispatch(t, "which plugins a run loads under --pure")
}

// TestOpenCodeInlineDenyBeatsProjectAllow pins question 2 with `opencode
// debug agents --standalone`: the build agent's resolved shell rules. Observed
// on 2.2.7, a layer's permissions list is appended after the layer below it,
// the inline document last, and the last matching rule wins, so the inline
// list's own last word decides:
//
//   - scalar: the project's `* allow` is followed by the inline `* deny`.
//   - pattern: the project's `* allow`, then the inline `* allow` and
//     `rm -rf * deny`, so `rm -rf x` is denied.
//   - reordered: the project lists `rm -rf * allow` before `* allow`; the
//     inline list still ends with its own `rm -rf * deny`. 1.x merged the map
//     in the lowest layer's key order and let `rm -rf x` run here (a KNOWN
//     hole); 2.x's appended list closes it.
//
// 2.x has no `debug agent --tool`, so no rule is run here: which tool calls a
// dispatch refuses needs `opencode run` (#3303) and 2.x's permission
// semantics (#3304).
func TestOpenCodeInlineDenyBeatsProjectAllow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		projectRules []string // the project alone, the control that it loads
		mergedRules  []string // project plus inline
	}{
		{"scalar", []string{"*=allow"}, []string{"*=allow", "*=deny"}},
		{"pattern", []string{"*=allow"}, []string{"*=allow", "*=allow", "rm -rf *=deny"}},
		{"reordered", []string{"rm -rf *=allow", "*=allow"}, []string{"rm -rf *=allow", "*=allow", "*=allow", "rm -rf *=deny"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newMCHarness(t, nil)
			fixture := "q2-permission-precedence/" + tc.name
			proj := h.project(fixture + "/project")
			inline := h.inline(fixture+"/inline.json", proj)

			if got := mcAgentRules(h.mcAgents(proj, "", nil)["build"], "shell"); !slices.Equal(got, tc.projectRules) {
				t.Fatalf("control: the project alone resolves shell to %q, want %q", got, tc.projectRules)
			}
			if got := mcAgentRules(h.mcAgents(proj, inline, nil)["build"], "shell"); !slices.Equal(got, tc.mergedRules) {
				t.Errorf("project plus inline resolves shell to %q, want %q", got, tc.mergedRules)
			}
		})
	}
}

// TestOpenCodeArrayMerge pins question 3. instructions and plugin
// concatenate, the lower layer first, and mcp merges by server name with the
// inline entry winning for a name both set. An empty inline list removes
// nothing (KNOWN): `debug config` then reports `plugin: []`, but its
// plugin_origins still lists the project's plugin, and the plugin loads; and
// a project's remote instructions URL is fetched.
func TestOpenCodeArrayMerge(t *testing.T) {
	t.Parallel()
	// 2.x's `debug config` lists each source's own arrays and no merged one,
	// so how instructions, plugins and mcp combine shows only in what a run
	// sends and loads.
	mcRunNeedsDispatch(t, "how instructions, plugins and mcp combine across layers")
}

// TestOpenCodeProviderBaseURLPrecedence pins question 4. The project's
// provider.<key>.options.baseURL (an RFC 5737 address, never routed) loses to
// the inline one, and a key the inline block does not set, a header, still
// comes from the project. The run confirms it on the wire: pointed at the
// model stub, the request reaches the stub and carries the project's header.
func TestOpenCodeProviderBaseURLPrecedence(t *testing.T) {
	t.Parallel()
	// 2.x's `debug config` prints no merged provider block, so which baseURL
	// wins shows only on the wire.
	mcRunNeedsDispatch(t, "which provider baseURL a run sends to")
}

// mcGit runs git through internal/gittest, which disarms background gc and
// blinds the invocation to the operator's or CI image's system/global config
// (#680, #542) instead of this file re-deriving that isolation on its own.
func (h *mcHarness) git(dir string, args ...string) {
	h.t.Helper()
	gittest.Run(h.t, dir, args...)
}

// TestOpenCodeConfigDiscoveryAboveWorktree pins question 5 on the layout
// worktree.go creates: a checkout with the worktree nested inside it at
// .nightgauge/worktrees/<repo>-issue-<N>, and config both in the checkout and
// in the directory above it, read through `debug config --standalone`'s
// sources and `debug agents --standalone`.
//
// KNOWN, changed on 2.2.7 and unchanged on 2.3.2: discovery does not stop at
// a git root. 1.18.30 stopped at the worktree's own root; 2.2.7 and 2.3.2 walk
// up past the worktree and the checkout, so from the worktree the checkout's
// opencode.json and .opencode/ and the opencode.json above the checkout all
// load. A dispatch sets OPENCODE_DISABLE_PROJECT_CONFIG=1
// (InstallNightgaugePlugin), and the last arm proves that switch drops every
// one of those sources from the worktree as well, on 2.3.2 as on 2.2.7. Whether the AGENTS.md files above reach a model request
// needs a 2.x run (#3303).
func TestOpenCodeConfigDiscoveryAboveWorktree(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	base := filepath.Join(h.scratch, "workspace")
	checkout := filepath.Join(base, "checkout")
	worktree := filepath.Join(checkout, ".nightgauge", "worktrees", "checkout-issue-1")
	mcMkdir(t, checkout)
	h.git(checkout, "init", "-q")
	h.git(checkout, "commit", "-q", "--allow-empty", "-m", "fixture")
	h.git(checkout, "worktree", "add", "-q", "-b", "issue-1", worktree)
	// Written after the worktree exists and never committed, so the worktree
	// does not check them out as its own files.
	h.copyFixture("q5-discovery-above-worktree/above", base, base)
	h.copyFixture("q5-discovery-above-worktree/checkout", checkout, checkout)
	agents := func(dir string, extra map[string]string) []string { return h.mcFixtureAgents(dir, "", extra) }
	sources := func(dir string, extra map[string]string) []string {
		return mcSourcePaths(h.debugConfig(dir, "", extra), base)
	}
	above := []string{"document:opencode.json", "document:checkout/opencode.json", "directory:checkout/.opencode"}
	aboveAgents := []string{"above-checkout-marker-agent", "checkout-dir-agent", "checkout-marker-agent"}

	// Control: in the checkout itself, its config and the config above load.
	if got := sources(checkout, nil); !slices.Equal(got, above) {
		t.Fatalf("control: sources in the checkout = %q, want %q", got, above)
	}
	if got := agents(checkout, nil); !slices.Equal(got, aboveAgents) {
		t.Fatalf("control: agents in the checkout = %q, want %q", got, aboveAgents)
	}

	if got := sources(worktree, nil); !slices.Equal(got, above) {
		t.Errorf("KNOWN changed: from the worktree, sources = %q, want everything above it %q", got, above)
	}
	if got := agents(worktree, nil); !slices.Equal(got, aboveAgents) {
		t.Errorf("KNOWN changed: from the worktree, agents = %q, want %q", got, aboveAgents)
	}

	h.copyFixture("q5-discovery-above-worktree/worktree", worktree, worktree)
	wantOwn := []string{above[0], above[1], "document:checkout/.nightgauge/worktrees/checkout-issue-1/opencode.json", above[2]}
	if got := sources(worktree, nil); !slices.Equal(got, wantOwn) {
		t.Errorf("with config of its own, sources in the worktree = %q, want %q", got, wantOwn)
	}
	if got, want := agents(worktree, nil), append(slices.Clone(aboveAgents), "worktree-marker-agent"); !slices.Equal(got, want) {
		t.Errorf("with config of its own, agents in the worktree = %q, want %q", got, want)
	}

	// What a dispatch sees: the switch drops the worktree's config and
	// everything discovery found above it.
	off := map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"}
	if got := sources(worktree, off); len(got) != 0 {
		t.Errorf("with OPENCODE_DISABLE_PROJECT_CONFIG=1, sources from the worktree = %q, want none", got)
	}
	if got := agents(worktree, off); len(got) != 0 {
		t.Errorf("with OPENCODE_DISABLE_PROJECT_CONFIG=1, agents from the worktree = %q, want none", got)
	}

	// Outside any git repository discovery walks up as well.
	loose := filepath.Join(base, "loose", "inner")
	mcMkdir(t, loose)
	if got := agents(loose, nil); !slices.Equal(got, []string{"above-checkout-marker-agent"}) {
		t.Errorf("outside git, agents in a nested directory = %q, want the config above it", got)
	}
}

// TestOpenCodeDisableProjectConfig pins question 6, read through `debug config
// --standalone`'s sources and `debug agents --standalone`:
// OPENCODE_DISABLE_PROJECT_CONFIG drops the project's opencode.json with every
// key the fixture sets (agents, permissions, instructions, plugins, mcp,
// providers, written as 1.x keys and translated by 2.x), its .opencode/
// directory and .opencode/opencode.json, and every agent they define. The
// inline document, with an instructions entry pointing into the project, is
// still a source. Whether the project's AGENTS.md and plugins reach a run
// needs a 2.x run (#3302, #3303).
func TestOpenCodeDisableProjectConfig(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	proj := h.project("q6-disable-project-config/project")
	inline := h.inline("q6-disable-project-config/inline.json", proj)
	off := map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"}
	projectDoc := func(sources []mcSource) map[string]any {
		for _, src := range sources {
			if paths := mcSourcePaths([]mcSource{src}, proj); len(paths) == 1 && paths[0] == "document:opencode.json" {
				return src.Info
			}
		}
		return nil
	}

	// Control: without the switch every fixture source and agent loads.
	srcs := h.debugConfig(proj, inline, nil)
	if got, want := mcSourcePaths(srcs, proj), []string{"document:opencode.json", "document:.opencode/opencode.json", "directory:.opencode"}; !slices.Equal(got, want) {
		t.Fatalf("control: project sources = %q, want %q", got, want)
	}
	if got, want := mcKeys(projectDoc(srcs)), []string{"$schema", "agents", "instructions", "mcp", "permissions", "plugins", "providers"}; !slices.Equal(got, want) {
		t.Fatalf("control: the project document's keys = %q, want %q", got, want)
	}
	if got, want := h.mcFixtureAgents(proj, inline, nil), []string{"dot-opencode-json-agent", "project-dir-agent", "project-json-agent"}; !slices.Equal(got, want) {
		t.Fatalf("control: agents = %q, want %q", got, want)
	}

	srcs = h.debugConfig(proj, inline, off)
	if got := mcSourcePaths(srcs, proj); len(got) != 0 {
		t.Errorf("with the switch, project sources still load: %q", got)
	}
	if got := h.mcFixtureAgents(proj, inline, off); len(got) != 0 {
		t.Errorf("with the switch, project agents still load: %q", got)
	}
	if got := mcStrings(mcInlineDocument(srcs)["instructions"]); len(got) != 1 || !strings.HasPrefix(got[0], proj) {
		t.Errorf("with the switch, the inline instructions = %q, want the one entry into the project", got)
	}
}

// TestOpenCodeMergeHarnessReapsEveryProcess proves the hygiene the suite
// relies on with the real binary. `opencode serve` on a loopback port the
// kernel picks never exits on its own (2.x has no `debug wait`): under a
// short cap it must be killed with its process group, and the ledger's
// kill -0 check must see it alive before and gone after.
func TestOpenCodeMergeHarnessReapsEveryProcess(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	h.timeout = 3 * time.Second
	p := h.start(h.scratch, nil, "", "serve", "--hostname", "127.0.0.1", "--port", "0")
	pid := p.cmd.Process.Pid
	if !mcSignalable(pid) {
		t.Fatalf("kill -0 does not see the running opencode (pid %d), so the ledger could never report a survivor", pid)
	}
	if !slices.Contains(h.pids.recorded(), pid) {
		t.Fatalf("the ledger did not record pid %d", pid)
	}
	res := p.wait()
	if !res.timedOut {
		t.Fatalf("`opencode serve` ended before the cap (exit %d); the cap path is untested", res.exit)
	}
	// The cap killed the group and Wait reaped the leader; allow the kernel a
	// moment to finish off the rest of the group.
	deadline := time.Now().Add(5 * time.Second)
	for alive := h.pids.survivors(); len(alive) != 0; alive = h.pids.survivors() {
		if time.Now().After(deadline) {
			t.Fatalf("the capped process or its group is still alive: %v", alive)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mcSignalable(pid) || mcSignalable(-pid) {
		t.Fatalf("kill -0 still reaches pid %d or its group after the cap", pid)
	}
	// The cap recorded what the group was doing before it killed it (#2454).
	for _, want := range []string{
		fmt.Sprintf("after start (pid %d):\n", pid),
		fmt.Sprintf("process group and descendants (pid ppid pgid stat elapsed cpu command):\n  %d ", pid),
		"serve --hostname 127.0.0.1",
		"open sockets and files",
		"OpenCode locks",
		"npm registry stub requests since start",
		"OpenCode log",
	} {
		if !strings.Contains(p.diagnosis, want) {
			t.Errorf("the diagnosis taken at the cap does not contain %q:\n%s", want, p.diagnosis)
		}
	}
}

// TestOpenCodeMergeHarnessReclaimsOrphanedLocks pins #2454 with the real
// binary: a lock a finished opencode left on its npm install must not hold
// up the next call. The lock is planted the way OpenCode writes one, owned by
// the harness's own `opencode --version`, which has exited, with its
// heartbeat dated ahead so that OpenCode's own minute-old staleness rule
// cannot free it while the test runs. q3's project config names a plugin, so
// `debug config` there waits on that install: without the harness's reclaim
// it reaches the cap; with it, it answers in seconds and the planted lock is
// gone.
func TestOpenCodeMergeHarnessReclaimsOrphanedLocks(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	proj := h.project("q3-array-merge/project")
	owner := h.pids.recorded()[0]
	if mcSignalable(owner) {
		t.Fatalf("the harness's opencode --version (pid %d) is still alive", owner)
	}
	key := "npm-install:" + filepath.Join(h.root, "config", "opencode")
	sum := sha1.Sum([]byte(key))
	lock := filepath.Join(h.locksDir(), hex.EncodeToString(sum[:])+".lock")
	mcMkdir(t, lock)
	const token = "2454-orphaned-lock"
	meta := fmt.Sprintf(`{"token":%q,"pid":%d,"hostname":"fixture","createdAt":%q}`, token, owner, time.Now().UTC().Format(time.RFC3339Nano))
	heartbeat := filepath.Join(lock, "heartbeat")
	for path, body := range map[string]string{filepath.Join(lock, "meta.json"): meta, heartbeat: ""} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ahead := time.Now().Add(time.Hour)
	if err := os.Chtimes(heartbeat, ahead, ahead); err != nil {
		t.Fatal(err)
	}

	var plugins []string
	for _, src := range h.debugConfig(proj, "", nil) {
		plugins = append(plugins, mcStrings(src.Info["plugins"])...)
	}
	if len(plugins) == 0 {
		t.Fatalf("control: q3's project config names no plugin, so nothing waited on the install")
	}
	if raw, err := os.ReadFile(filepath.Join(lock, "meta.json")); err == nil && strings.Contains(string(raw), token) {
		t.Errorf("the planted lock is still in place after debug config: %s", raw)
	}
}

// TestOpenCodeBuilderDocumentSurvives (#3301): 2.x drops an unknown key, an
// untranslated 1.x key and a wrong-typed value without a word, and 1.x's
// subagent_depth was dropped that way. The document BuildOpenCodeConfig
// writes, with the files it refers to in place, must come back from `debug
// config --standalone` key for key (apiKey masked, as 2.x prints it), and
// `debug agents --standalone` must resolve the subagent action to deny on
// every subagent-mode agent, the last matching rule.
func TestOpenCodeBuilderDocumentSurvives(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	settings := config.OpenCodeConfig{Endpoints: []config.OpenCodeEndpointConfig{{
		ID: "mtplx", Provider: "openai-compatible", BaseURL: "http://127.0.0.1:9/v1",
		Limit: config.OpenCodeLimit{Context: 131072, Output: 8192},
		Models: []config.OpenCodeEndpointModel{{ID: "qwen/qwen3.8-27b", Variants: []string{"low", "high"},
			RequestOptions: map[string]any{"temperature": 0.2}}},
	}}}
	runRoot := filepath.Join(h.scratch, "per-run-root")
	in, err := OpenCodeConfigInputFor(settings, RunOptions{
		Stage: "feature-dev", Model: "mtplx/qwen/qwen3.8-27b", MaxTurns: 40,
		WorktreeDir: h.scratch, AllowedTools: []string{"Read", "Edit", "Bash(git *)", "Task"},
	}, runRoot, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	built, err := BuildOpenCodeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range built.Files {
		mcMkdir(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var want map[string]any
	if err := json.Unmarshal([]byte(built.Content), &want); err != nil {
		t.Fatal(err)
	}
	settingsBlock := want["providers"].(map[string]any)["mtplx"].(map[string]any)["settings"].(map[string]any)
	settingsBlock["apiKey"] = "***"
	settingsBlock["baseURL"] = built.Files[openCodeEndpointURLFile(runRoot, "mtplx")]
	got := mcInlineDocument(h.debugConfig(h.scratch, built.Content, nil))
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("opencode %s did not load the builder's document intact.\nwrote: %s\nloaded: %s", mcVersion, wantJSON, gotJSON)
	}

	agents := h.mcAgents(h.scratch, built.Content, nil)
	for _, name := range openCodeSubagentAgents {
		rules := mcAgentRules(agents[name], "subagent")
		if len(rules) == 0 || rules[len(rules)-1] != "*=deny" {
			t.Errorf("agent %s resolves subagent to %q; the last rule must deny it, so a subagent cannot start another", name, rules)
		}
		if mode := agents[name]["mode"]; mode != "subagent" {
			t.Errorf("agent %s has mode %v, want subagent: openCodeSubagentAgents no longer matches the built-in agents", name, mode)
		}
	}
	// The document's own subagent rule (1.x's task, denied whatever the stage
	// grants: ADR-022 AC9) reaches the build agent too.
	if rules := mcAgentRules(agents["build"], "subagent"); len(rules) == 0 || rules[len(rules)-1] != "*=deny" {
		t.Errorf("the build agent resolves subagent to %q, want the document's deny last", rules)
	}
}
