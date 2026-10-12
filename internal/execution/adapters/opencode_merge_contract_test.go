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

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// mcVersion is the opencode version every answer here was observed on. A
// different binary fails rather than skips: its behaviour is unverified.
const mcVersion = "1.18.30"

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
	if v := strings.TrimSpace(res.stdout); v != mcVersion {
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
	res := h.run(dir, extra, "", append([]string{"debug"}, args...)...)
	if res.exit != 0 {
		h.t.Fatalf("opencode debug %s exited %d:\n%s\n%s", strings.Join(args, " "), res.exit, res.stdout, res.stderr)
	}
	if err := json.Unmarshal([]byte(res.stdout), out); err != nil {
		h.t.Fatalf("opencode debug %s printed no JSON: %v\n%s", strings.Join(args, " "), err, res.stdout)
	}
}

// debugConfig is `opencode debug config` in dir: the merged config.
func (h *mcHarness) debugConfig(dir, inline string, extra map[string]string) map[string]any {
	h.t.Helper()
	cfg := map[string]any{}
	h.debugJSON(dir, withInline(inline, extra), &cfg, "config")
	return cfg
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
	args = append(args, "--format", "json", "--print-logs", "--log-level", "ERROR", "-m", mcUnknownModel)
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
	res := h.run(dir, env, mcPrompt, "run", "--format", "json", "--print-logs", "--log-level", "ERROR", "-m", mcStubModel)
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
	const npmPlugin = mcNpmPluginPackage
	packages := map[string]string{npmPlugin: filepath.Join(mcFixtures, "q1-pure-plugins", "npm-plugin")}

	control := newMCHarness(t, packages)
	lines := control.runUnknownModel(control.project("q1-pure-plugins/project"), "", nil)
	if want := []string{"npm-plugin", "project-dir-plugin"}; !slices.Equal(lines, want) {
		t.Fatalf("positive control did not fire: without --pure the fixture plugins wrote %q, want %q. "+
			"The harness is invalid, so a silent --pure run would prove nothing", lines, want)
	}
	for _, want := range []string{"/" + npmPlugin, mcPluginSDKPath} {
		if !slices.Contains(control.registry.requests(), want) {
			t.Fatalf("positive control: the npm registry stub never saw %s requested: %q", want, control.registry.requests())
		}
	}

	pure := newMCHarness(t, packages)
	if lines := pure.runUnknownModel(pure.project("q1-pure-plugins/project"), "", nil, "--pure"); len(lines) != 0 {
		t.Errorf("with --pure the fixture plugins still loaded and wrote %q", lines)
	}
	for _, req := range pure.registry.requests() {
		if req != mcPluginSDKPath {
			t.Errorf("with --pure OpenCode still asked the npm registry for %s", req)
		}
	}

	// A run with no plugin anywhere, kept alive by a model stub that answers
	// only once the registry has been asked, or after ten seconds.
	plain := newMCHarness(t, nil)
	proj := filepath.Join(plain.scratch, "no-plugins")
	mcMkdir(t, proj)
	plain.git(proj, "init", "-q")
	stub := newMCModelStub(t, func() bool { return len(plain.registry.requests()) > 0 })
	if _, lines := plain.runStubModel(proj, "", stub, nil); len(lines) != 0 {
		t.Fatalf("a run with no plugins loaded %q", lines)
	}
	reqs := slices.Compact(slices.Sorted(slices.Values(plain.registry.requests())))
	if !slices.Equal(reqs, []string{mcPluginSDKPath}) {
		t.Errorf("a run with no plugins asked the npm registry for %q, want the background install of %s alone", reqs, mcPluginSDKPath)
	}
}

// mcBashRules returns the bash rules of an agent's resolved permission
// ruleset, in order, as "pattern=action".
func mcBashRules(agent map[string]any) []string {
	var rules []string
	list, _ := agent["permission"].([]any)
	for _, r := range list {
		rule, _ := r.(map[string]any)
		if rule["permission"] == "bash" {
			rules = append(rules, fmt.Sprintf("%v=%v", rule["pattern"], rule["action"]))
		}
	}
	return rules
}

// TestOpenCodeInlineDenyBeatsProjectAllow pins question 2 with
// `opencode debug agent build`: its resolved ruleset, and its --tool mode,
// which runs the bash tool on `rm -rf x` in the project under that ruleset.
//
//   - scalar: the project's `bash: allow` loses to the inline `bash: deny`,
//     and a whole-tool deny takes bash out of the agent's tools.
//   - pattern: the project's `{"*": "allow"}` and the inline
//     `{"rm -rf *": "deny"}` merge into one map, the inline pattern last, and
//     the last matching rule wins, so `rm -rf x` is denied.
//   - reordered (KNOWN hole): the merged map keeps the key order of the lowest
//     layer that has the key, so a project that lists `rm -rf *` before `*`
//     puts the inline deny first and the catch-all allow last, and `rm -rf x`
//     runs. An inline value wins per key; its position does not.
//
// Negative control: with scalar/inline.json flipped to `"bash": "allow"` the
// scalar case goes red, because the tool runs and x is removed.
func TestOpenCodeInlineDenyBeatsProjectAllow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		projectRules []string // the project alone, the control that it loads
		mergedRules  []string // project plus inline
		refusal      string   // how `rm -rf x` is refused; empty when it runs
	}{
		{"scalar", []string{"*=allow"}, []string{"*=deny"}, "Tool bash is disabled for agent build"},
		{"pattern", []string{"*=allow"}, []string{"*=allow", "rm -rf *=deny"}, "prevents you from using this specific tool call"},
		{"reordered", []string{"rm -rf *=allow", "*=allow"}, []string{"rm -rf *=deny", "*=allow"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newMCHarness(t, nil)
			fixture := "q2-permission-precedence/" + tc.name
			proj := h.project(fixture + "/project")
			inline := h.inline(fixture+"/inline.json", proj)
			keep := filepath.Join(proj, "x", "keep.txt")
			mcMkdir(t, filepath.Dir(keep))
			if err := os.WriteFile(keep, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			agent := map[string]any{}
			h.debugJSON(proj, nil, &agent, "agent", "build")
			if got := mcBashRules(agent); !slices.Equal(got, tc.projectRules) {
				t.Fatalf("control: the project alone resolves bash to %q, want %q", got, tc.projectRules)
			}
			agent = map[string]any{}
			h.debugJSON(proj, withInline(inline, nil), &agent, "agent", "build")
			if got := mcBashRules(agent); !slices.Equal(got, tc.mergedRules) {
				t.Errorf("project plus inline resolves bash to %q, want %q", got, tc.mergedRules)
			}

			params := `{"command":"rm -rf x","description":"adversarial fixture"}`
			res := h.run(proj, withInline(inline, nil), "", "debug", "agent", "build", "--tool", "bash", "--params", params)
			_, statErr := os.Stat(keep)
			if tc.refusal != "" {
				if res.exit == 0 || !strings.Contains(res.stdout+res.stderr, tc.refusal) || statErr != nil {
					t.Errorf("`rm -rf x` was not denied with %q (exit %d, x kept: %v):\n%s\n%s", tc.refusal, res.exit, statErr == nil, res.stdout, res.stderr)
				}
				return
			}
			if res.exit != 0 || !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("KNOWN hole changed: `rm -rf x` was expected to run (exit %d, x kept: %v):\n%s\n%s", res.exit, statErr == nil, res.stdout, res.stderr)
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
	h := newMCHarness(t, nil)
	proj := h.project("q3-array-merge/project")
	inline := h.inline("q3-array-merge/inline.json", proj)
	projectPlugin := "file://" + proj + "/plugins/project-plugin.js"
	inlinePlugin := "file://" + proj + "/plugins/inline-plugin.js"

	cfg := h.debugConfig(proj, inline, nil)
	if got, want := mcStrings(cfg["instructions"]), []string{"rules-one.md", "rules-two.md", proj + "/inline-rules.md"}; !slices.Equal(got, want) {
		t.Errorf("instructions = %q, want the project's two then the inline one %q", got, want)
	}
	if got, want := mcStrings(cfg["plugin"]), []string{projectPlugin, inlinePlugin}; !slices.Equal(got, want) {
		t.Errorf("plugin = %q, want %q", got, want)
	}
	if got, want := mcKeys(cfg["mcp"]), []string{"inline-mcp", "project-mcp", "shared-mcp"}; !slices.Equal(got, want) {
		t.Errorf("mcp servers = %q, want %q", got, want)
	}
	if got := mcStrings(mcDig(cfg, "mcp", "shared-mcp", "command")); !slices.Equal(got, []string{"/usr/bin/true"}) {
		t.Errorf("mcp.shared-mcp.command = %q, want the inline command alone, not concatenated", got)
	}

	stub := newMCModelStub(t, nil)
	_, lines := h.runStubModel(proj, inline, stub, nil)
	if want := []string{"inline-list-plugin", "project-list-plugin"}; !slices.Equal(lines, want) {
		t.Errorf("plugins loaded = %q, want both %q", lines, want)
	}
	body := stub.bodies()
	for _, marker := range []string{"ADVERSARIAL_RULES_ONE_MARKER", "ADVERSARIAL_RULES_TWO_MARKER", "ADVERSARIAL_INLINE_RULES_MARKER"} {
		if !strings.Contains(body, marker) {
			t.Errorf("the model request does not carry %s, so that instructions entry did not load", marker)
		}
	}

	empty := h.inline("q3-array-merge/inline-empty.json", proj)
	cfg = h.debugConfig(proj, empty, nil)
	if got, want := mcStrings(cfg["instructions"]), []string{"rules-one.md", "rules-two.md"}; !slices.Equal(got, want) {
		t.Errorf("with an empty inline list, instructions = %q, want the project's %q", got, want)
	}
	if got := mcStrings(cfg["plugin"]); len(got) != 0 {
		t.Errorf("KNOWN: with an empty inline list, debug config's plugin was expected to read [], got %q", got)
	}
	origins, _ := cfg["plugin_origins"].([]any)
	var specs []string
	for _, o := range origins {
		specs = append(specs, fmt.Sprint(mcDig(o, "spec")))
	}
	if !slices.Equal(specs, []string{projectPlugin}) {
		t.Errorf("KNOWN: with an empty inline list, plugin_origins = %q, want the project's plugin still listed", specs)
	}
	if lines := h.runUnknownModel(proj, empty, nil); !slices.Equal(lines, []string{"project-list-plugin"}) {
		t.Errorf("KNOWN: with an empty inline plugin list the project's plugin was expected to load anyway; plugins loaded = %q", lines)
	}

	// A remote instructions entry concatenates too, and is fetched (KNOWN): a
	// project that names a URL, here the loopback model stub's, makes the run
	// request it before its model request, whatever the inline list says.
	remote := newMCModelStub(t, nil)
	remoteProj := h.project("q3-array-merge/remote-project")
	remoteCfg := filepath.Join(remoteProj, "opencode.json")
	raw, err := os.ReadFile(remoteCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteCfg, []byte(strings.ReplaceAll(string(raw), mcRemoteURL, remote.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteURL := remote.URL + mcRemoteInstructionPath
	remoteEmpty := h.inline("q3-array-merge/inline-empty.json", remoteProj)
	if got := mcStrings(h.debugConfig(remoteProj, remoteEmpty, nil)["instructions"]); !slices.Equal(got, []string{remoteURL}) {
		t.Errorf("KNOWN: with an empty inline list, instructions = %q, want the project's %q", got, remoteURL)
	}
	h.runStubModel(remoteProj, remoteEmpty, remote, nil)
	var paths []string
	for _, r := range remote.requests() {
		paths = append(paths, r.path)
	}
	fetched := slices.Index(paths, mcRemoteInstructionPath)
	if model := slices.Index(paths, "/v1/chat/completions"); fetched < 0 || (model >= 0 && model < fetched) {
		t.Errorf("KNOWN: the project's remote instructions entry was expected to be fetched before the model request; the stub saw %q", paths)
	}
}

// TestOpenCodeProviderBaseURLPrecedence pins question 4. The project's
// provider.<key>.options.baseURL (an RFC 5737 address, never routed) loses to
// the inline one, and a key the inline block does not set, a header, still
// comes from the project. The run confirms it on the wire: pointed at the
// model stub, the request reaches the stub and carries the project's header.
func TestOpenCodeProviderBaseURLPrecedence(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	proj := h.project("q4-provider-baseurl/project")
	inline := h.inline("q4-provider-baseurl/inline.json", proj)
	baseURL := func(cfg map[string]any) any {
		return mcDig(cfg, "provider", "fixture-endpoint", "options", "baseURL")
	}

	if got := baseURL(h.debugConfig(proj, "", nil)); got != "http://192.0.2.1:9/v1" {
		t.Fatalf("control: the project alone gives baseURL %v", got)
	}
	cfg := h.debugConfig(proj, inline, nil)
	if got := baseURL(cfg); got != "http://127.0.0.1:1234/v1" {
		t.Errorf("baseURL = %v, want the inline http://127.0.0.1:1234/v1", got)
	}
	if got := mcDig(cfg, "provider", "fixture-endpoint", "options", "headers", "X-Adversarial-Fixture"); got != "project" {
		t.Errorf("the project's header = %v, want it merged in beside the inline block", got)
	}

	stub := newMCModelStub(t, nil)
	res, _ := h.runStubModel(proj, inline, stub, nil)
	for _, r := range stub.requests() {
		if r.path != "/v1/chat/completions" || r.header.Get("X-Adversarial-Fixture") != "project" {
			t.Errorf("the stub saw %s with X-Adversarial-Fixture %q, want /v1/chat/completions with the project's header", r.path, r.header.Get("X-Adversarial-Fixture"))
		}
	}
	if want := stub.URL + "/v1/chat/completions"; !strings.Contains(res.stdout, want) {
		t.Errorf("the run's error event does not name %s:\n%s", want, res.stdout)
	}
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
// .nightgauge/worktrees/<repo>-issue-<N>, and config and AGENTS.md both in the
// checkout and in the directory above it. From the worktree, discovery stops
// at the worktree's own root: neither the checkout's config and rules nor
// anything above them loads. The controls show the fixtures load where they
// are in scope.
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
	agents := func(dir string) []string { return mcKeys(h.debugConfig(dir, "", nil)["agent"]) }
	stub := newMCModelStub(t, nil)
	rules := func(dir string) []string {
		before := len(stub.requests())
		h.runStubModel(dir, "", stub, nil)
		var found []string
		for _, r := range stub.requests()[before:] {
			for _, m := range []string{"ADVERSARIAL_ABOVE_CHECKOUT_RULES_MARKER", "ADVERSARIAL_CHECKOUT_RULES_MARKER", "ADVERSARIAL_WORKTREE_RULES_MARKER"} {
				if strings.Contains(r.body, m) && !slices.Contains(found, m) {
					found = append(found, m)
				}
			}
		}
		slices.Sort(found)
		return found
	}

	// Controls: in the checkout itself, its config and rules load. Discovery
	// stops at the checkout's git root, so the directory above stays out.
	if got, want := agents(checkout), []string{"checkout-dir-agent", "checkout-marker-agent"}; !slices.Equal(got, want) {
		t.Fatalf("control: agents in the checkout = %q, want %q", got, want)
	}
	if got, want := rules(checkout), []string{"ADVERSARIAL_CHECKOUT_RULES_MARKER"}; !slices.Equal(got, want) {
		t.Fatalf("control: rules in the checkout = %q, want %q", got, want)
	}

	if got := agents(worktree); len(got) != 0 {
		t.Errorf("from the worktree, config above its root loaded: agents %q", got)
	}
	if got := rules(worktree); len(got) != 0 {
		t.Errorf("from the worktree, rules above its root reached the model request: %q", got)
	}

	h.copyFixture("q5-discovery-above-worktree/worktree", worktree, worktree)
	if got, want := agents(worktree), []string{"worktree-marker-agent"}; !slices.Equal(got, want) {
		t.Errorf("with config of its own, agents in the worktree = %q, want %q", got, want)
	}
	if got, want := rules(worktree), []string{"ADVERSARIAL_WORKTREE_RULES_MARKER"}; !slices.Equal(got, want) {
		t.Errorf("with rules of its own, rules in the worktree = %q, want %q", got, want)
	}

	// Outside any git repository there is no root to stop at, and discovery
	// walks up: the config above loads from a directory beneath it.
	loose := filepath.Join(base, "loose", "inner")
	mcMkdir(t, loose)
	if got := agents(loose); !slices.Contains(got, "above-checkout-marker-agent") {
		t.Errorf("outside git, agents in a nested directory = %q, want the config above it included", got)
	}
}

// TestOpenCodeDisableProjectConfig pins question 6: OPENCODE_DISABLE_PROJECT_CONFIG
// drops every project key the fixture sets (agent, permission, instructions,
// plugin, mcp, provider), everything under .opencode/ (its opencode.json,
// agents, commands, skills and plugins), the project's AGENTS.md, and the
// plugin loads with them. An inline instructions entry with an absolute path
// into the project still loads.
func TestOpenCodeDisableProjectConfig(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	proj := h.project("q6-disable-project-config/project")
	inline := h.inline("q6-disable-project-config/inline.json", proj)
	off := map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"}
	skills := func(extra map[string]string) []string {
		var list []map[string]any
		h.debugJSON(proj, extra, &list, "skill")
		var names []string
		for _, s := range list {
			names = append(names, fmt.Sprint(s["name"]))
		}
		slices.Sort(names)
		return names
	}
	stub := newMCModelStub(t, nil)
	markers := func(extra map[string]string) ([]string, []string) {
		before := len(stub.requests())
		_, lines := h.runStubModel(proj, inline, stub, extra)
		var found []string
		for _, r := range stub.requests()[before:] {
			for _, m := range []string{"ADVERSARIAL_INLINE_STEERING_MARKER", "ADVERSARIAL_PROJECT_AGENTS_RULES_MARKER", "ADVERSARIAL_PROJECT_INSTRUCTIONS_MARKER"} {
				if strings.Contains(r.body, m) && !slices.Contains(found, m) {
					found = append(found, m)
				}
			}
		}
		slices.Sort(found)
		return found, lines
	}

	// Control: without the switch every fixture key loads.
	cfg := h.debugConfig(proj, "", nil)
	control := map[string][]string{
		"agent":      {"dot-opencode-json-agent", "project-dir-agent", "project-json-agent"},
		"command":    {"project-dir-command"},
		"mcp":        {"project-mcp"},
		"permission": {"bash"},
		"provider":   {"project-provider"},
	}
	for key, want := range control {
		if got := mcKeys(cfg[key]); !slices.Equal(got, want) {
			t.Fatalf("control: %s = %q, want %q", key, got, want)
		}
	}
	if got := mcStrings(cfg["instructions"]); !slices.Equal(got, []string{"project-rules.md"}) {
		t.Fatalf("control: instructions = %q", got)
	}
	if got := mcStrings(cfg["plugin"]); len(got) != 2 {
		t.Fatalf("control: plugin = %q, want the opencode.json entry and the .opencode/plugins file", got)
	}
	if got := skills(nil); !slices.Contains(got, "project-dir-skill") {
		t.Fatalf("control: skills = %q, want project-dir-skill", got)
	}
	found, lines := markers(nil)
	if want := []string{"ADVERSARIAL_INLINE_STEERING_MARKER", "ADVERSARIAL_PROJECT_AGENTS_RULES_MARKER", "ADVERSARIAL_PROJECT_INSTRUCTIONS_MARKER"}; !slices.Equal(found, want) {
		t.Fatalf("control: the model request carries %q, want %q", found, want)
	}
	if want := []string{"project-dir-plugin", "project-json-plugin"}; !slices.Equal(lines, want) {
		t.Fatalf("control: plugins loaded = %q, want %q", lines, want)
	}

	cfg = h.debugConfig(proj, "", off)
	for _, key := range []string{"agent", "command", "mcp", "permission", "provider", "instructions", "plugin"} {
		if v, ok := cfg[key]; ok && len(mcKeys(v))+len(mcStrings(v)) > 0 {
			t.Errorf("with the switch, %s is still set: %v", key, v)
		}
	}
	if got := skills(off); slices.Contains(got, "project-dir-skill") {
		t.Errorf("with the switch, skills = %q still include the project's", got)
	}
	found, lines = markers(off)
	if want := []string{"ADVERSARIAL_INLINE_STEERING_MARKER"}; !slices.Equal(found, want) {
		t.Errorf("with the switch, the model request carries %q, want only the inline instructions %q", found, want)
	}
	if len(lines) != 0 {
		t.Errorf("with the switch, project plugins still loaded: %q", lines)
	}
}

// TestOpenCodeMergeHarnessReapsEveryProcess proves the hygiene the suite
// relies on with the real binary. `opencode debug wait` never exits on its
// own: under a short cap it must be killed with its process group, and the
// ledger's kill -0 check must see it alive before and gone after.
func TestOpenCodeMergeHarnessReapsEveryProcess(t *testing.T) {
	t.Parallel()
	h := newMCHarness(t, nil)
	h.timeout = 3 * time.Second
	p := h.start(h.scratch, nil, "", "debug", "wait")
	pid := p.cmd.Process.Pid
	if !mcSignalable(pid) {
		t.Fatalf("kill -0 does not see the running opencode (pid %d), so the ledger could never report a survivor", pid)
	}
	if !slices.Contains(h.pids.recorded(), pid) {
		t.Fatalf("the ledger did not record pid %d", pid)
	}
	res := p.wait()
	if !res.timedOut {
		t.Fatalf("`opencode debug wait` ended before the cap (exit %d); the cap path is untested", res.exit)
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
		"debug wait",
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

	cfg := h.debugConfig(proj, "", nil)
	if got := mcStrings(cfg["plugin"]); len(got) == 0 {
		t.Fatalf("control: q3's project config names no plugin, so nothing waited on the install: %v", cfg["plugin"])
	}
	if raw, err := os.ReadFile(filepath.Join(lock, "meta.json")); err == nil && strings.Contains(string(raw), token) {
		t.Errorf("the planted lock is still in place after debug config: %s", raw)
	}
}
