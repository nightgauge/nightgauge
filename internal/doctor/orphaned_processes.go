package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
)

// Leaked processes — nightgauge processes still running that no live sidecar
// claims (#341).
//
// Same family as the worktree and stash carriers next door, one layer down: a
// stage that is killed leaks its worktree, but a stage that is never killed
// leaks ITSELF. `nightgauge autonomous run --dry-run` sat on this machine for
// 31 hours holding a scheduler slot, and every doctor check passed the whole
// time — none of them enumerated processes, so the leak had no reporter at all.
//
// This carrier is REPORT-ONLY. It never signals anything: the operator decides
// what to terminate, and a health check that kills processes on the strength of
// an argv string is a far worse failure than the one it is fixing.

// staleProcessAge is how long a nightgauge process may run unclaimed before
// `doctor` mentions it. Every verb except `serve` and `autonomous run`
// finishes in minutes, so an hour is far past any transient CLI invocation and
// past the window in which a process can outrun its own sidecar write. The
// specimen that motivated this check had been up for 31 hours.
const staleProcessAge = 1 * time.Hour

// staleSidecarClaim is how recently a sidecar must have made progress for the
// PID it names to count as owned.
//
// Ownership is a PROGRESS test, not a presence test. Every long-lived verb
// writes its OWN pid into the sidecar it owns — `autonomous run` writes
// state.json (orchestrator.autonomousStateFile), the pipeline runner writes
// current-run.json — so treating a bare pid as ownership is self-attestation:
// the wedged 31-hour scheduler this check exists for claimed itself and read
// as owned forever.
//
// The window is deliberately far wider than the 1h staleProcessAge floor.
// Scan and stage cadence is operator-configurable (`autonomous run --interval`
// may legally exceed an hour), so a claim window at the age floor would report
// healthy, idle-but-scheduled schedulers. It mirrors staleWorktreeAge (24h)
// instead — the same "this machine state has stopped moving" threshold the
// worktree carrier uses — and the incident specimen (31h) still surfaces.
const staleSidecarClaim = 24 * time.Hour

// psTimeout bounds the enumeration so a wedged `ps` cannot hang `doctor`.
const psTimeout = 5 * time.Second

// runningProcess is one row of the process table.
type runningProcess struct {
	PID int
	Age time.Duration
	// Command is the full argv as `ps` reported it. Evidence for the operator
	// and the source of the subcommand token — never a claim of ownership.
	Command string
}

// firstToken returns argv[0], or "" for an empty command.
func (p runningProcess) firstToken() string {
	if i := strings.IndexByte(p.Command, ' '); i >= 0 {
		return p.Command[:i]
	}
	return p.Command
}

// isNightgauge reports whether this row is a nightgauge process.
//
// The test is the BASENAME of argv[0] and nothing else. Matching anywhere in
// the command line would claim every `grep nightgauge`, every editor with a
// nightgauge file open, and this very `doctor` run's shell — and reporting an
// operator's own terminal as an orphan is how a check gets ignored.
//
// Accepted limitation, in the same family as the recycled-PID edge below: `ps`
// does not delimit argv[0], so a binary installed under a path CONTAINING A
// SPACE ("/Applications/My Tools/nightgauge") splits at the space and its
// basename never reads as `nightgauge` — the process is invisible to this
// scan. That is inherent `ps` ambiguity, not a parser bug: nothing in the
// output distinguishes a space inside the executable path from the space
// before argv[1]. Under-reporting is the safe direction for a check whose only
// output is a warning.
func (p runningProcess) isNightgauge() bool {
	exe := p.firstToken()
	return exe != "" && filepath.Base(exe) == "nightgauge"
}

// subcommand returns the verb: the first token after argv[0] that does not
// start with `-`. "" when the process was invoked bare or carries only flags.
//
// Positional rather than strictly argv[1] because a global flag may precede
// the verb (`nightgauge --verbose serve`), and a verb this reader misses is a
// serve daemon reported as an orphan on every single doctor run.
func (p runningProcess) subcommand() string {
	fields := strings.Fields(p.Command)
	if len(fields) < 2 {
		return ""
	}
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") {
			return f
		}
	}
	return ""
}

// enumerateProcesses returns the raw process table.
//
// Thin by design: everything that decides anything lives in parseProcessTable,
// so the tests read a captured table instead of spawning one (and so the
// captured table is the only description of `ps` output in the package).
func enumerateProcesses() (string, error) {
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("no ps on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,etime=,command=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseProcessTable parses `ps -axo pid=,etime=,command=` output, returning the
// rows and whether the table was fully understood.
//
// One malformed row undetermines the WHOLE table — the same rule the worktree
// scanner applies to an unreadable root (execution.ActiveWorktreeIssues). A
// partially parsed process table is indistinguishable from a complete one at
// the call site, and the row this parser could not read is exactly as likely to
// be the leak as any other.
func parseProcessTable(raw string) ([]runningProcess, bool) {
	var procs []runningProcess
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidTok, rest, ok := splitToken(line)
		if !ok {
			return nil, false
		}
		etimeTok, command, ok := splitToken(rest)
		if !ok {
			return nil, false
		}
		pid, err := strconv.Atoi(pidTok)
		if err != nil || pid <= 0 {
			return nil, false
		}
		age, ok := parseETime(etimeTok)
		if !ok {
			return nil, false
		}
		procs = append(procs, runningProcess{PID: pid, Age: age, Command: command})
	}
	return procs, true
}

// splitToken peels the leading whitespace-delimited token off s, returning it
// and the remainder. ok=false when either side is empty — `ps` emits three
// populated columns or the row is not one this parser understands.
func splitToken(s string) (token, rest string, ok bool) {
	i := strings.IndexAny(s, " \t")
	if i <= 0 {
		return "", "", false
	}
	rest = strings.TrimLeft(s[i:], " \t")
	if rest == "" {
		return "", "", false
	}
	return s[:i], rest, true
}

// parseETime parses the three elapsed-time formats `ps` emits: mm:ss,
// hh:mm:ss, and dd-hh:mm:ss.
func parseETime(s string) (time.Duration, bool) {
	days := 0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil || d < 0 {
			return 0, false
		}
		days = d
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		nums[i] = n
	}
	hours := 0
	if len(nums) == 3 {
		hours, nums = nums[0], nums[1:]
	} else if days > 0 {
		// dd-mm:ss is not a shape `ps` produces; a day count without an hour
		// field means this row was not understood.
		return 0, false
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(nums[0])*time.Minute +
		time.Duration(nums[1])*time.Second, true
}

// sidecarRoots is every directory that can hold a run sidecar: the workspace's
// repo checkouts plus the WORKSPACE root itself.
//
// The two are not the same set and the difference is load-bearing.
// WorkspaceRepoRoots answers "which repo checkouts must a workspace-wide scan
// inspect?", and it never includes the workspace root unless a repo happens to
// live there — but the scheduler writes .nightgauge/autonomous/state.json
// relative to the WORKSPACE root. In a multi-repo workspace the one sidecar
// that can claim the long-lived scheduler sits in a directory the repo-root
// list does not contain.
func sidecarRoots(startDir string) []string {
	roots := config.WorkspaceRepoRoots(startDir)
	wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
	if err != nil {
		return roots
	}
	abs, err := filepath.Abs(wsRoot)
	if err != nil {
		return roots
	}
	abs = filepath.Clean(abs)
	for _, r := range roots {
		if r == abs {
			return roots
		}
	}
	return append(roots, abs)
}

// sidecarPIDs collects every PID claimed by a sidecar that has made recent
// progress, across every sidecar root: the autonomous scheduler's state.json,
// the in-flight run's current-run.json, and each attempt in run-state.json.
//
// A claim counts only when the sidecar's own progress timestamp is within
// staleSidecarClaim of now — see that constant for why presence alone cannot
// be ownership. Each file is read with the timestamp its writer actually
// records: the scheduler's per-cycle lastScanAt, the run sidecar's
// stage_started_at, run-state's updated_at, each falling back to its
// start-of-life stamp when the progress field has not been written yet.
//
// A missing, unparsable, or undated sidecar contributes no PIDs and does NOT
// undetermine the scan. Ownership is a narrowing filter here, so failing to
// read one fails toward UNOWNED, which fails toward REPORTING — safe for a
// report-only carrier in a way it would not be for anything that acted on the
// answer.
//
// Accepted edge: a dead writer's PID recycled by another nightgauge process
// reads as OWNED for as long as the claim stays fresh, and that process would
// go unreported. Deliberately not mitigated — the alternative is probing writer
// liveness per sidecar, which buys a rare false negative at the cost of a
// second liveness authority.
func sidecarPIDs(startDir string, now time.Time) map[int]bool {
	claimed := map[int]bool{}
	for _, root := range sidecarRoots(startDir) {
		// orchestrator.AutonomousState: lastScanAt is rewritten every scan
		// cycle, so it is the scheduler's proof of life.
		var autonomous struct {
			PID        int    `json:"pid"`
			StartedAt  string `json:"startedAt"`
			LastScanAt string `json:"lastScanAt"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "autonomous", "state.json"), &autonomous) &&
			progressIsFresh(now, autonomous.LastScanAt, autonomous.StartedAt) {
			claimPID(claimed, autonomous.PID)
		}

		// orchestrator.CurrentRunSidecar: rewritten at every stage start.
		var currentRun struct {
			PID        int    `json:"pid"`
			StartedAt  string `json:"started_at"`
			StageStart string `json:"stage_started_at"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "pipeline", "current-run.json"), &currentRun) &&
			progressIsFresh(now, currentRun.StageStart, currentRun.StartedAt) {
			claimPID(claimed, currentRun.PID)
		}

		// runstate.RunState: updated_at moves on every lifecycle transition.
		var runState struct {
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Attempts  []struct {
				PID *int `json:"pid"`
			} `json:"attempts"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "pipeline", "run-state.json"), &runState) &&
			progressIsFresh(now, runState.UpdatedAt, runState.CreatedAt) {
			for _, a := range runState.Attempts {
				if a.PID != nil {
					claimPID(claimed, *a.PID)
				}
			}
		}
	}
	return claimed
}

// progressIsFresh reports whether the first populated stamp (RFC3339, most
// recent-bearing field first) shows progress within staleSidecarClaim of now.
//
// No stamp at all, or one this reader cannot parse, is NOT fresh: an
// undated claim has proven nothing, and an unproven claim must fail toward
// REPORTING — the same direction every other unknown in this file fails, and
// safe only because this carrier never acts on the answer.
func progressIsFresh(now time.Time, stamps ...string) bool {
	for _, s := range stamps {
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return false
		}
		return now.Sub(t) < staleSidecarClaim
	}
	return false
}

// readJSONFile decodes path into v, reporting whether it produced anything
// usable. Minimal local structs on purpose: this is a READER of sidecars other
// packages own, and importing their types would make their schemas answerable
// to `doctor`.
func readJSONFile(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

func claimPID(claimed map[int]bool, pid int) {
	if pid > 0 {
		claimed[pid] = true
	}
}

// processScan is the classified process table behind the check's counts.
type processScan struct {
	// Scanned is every nightgauge process except this one.
	Scanned int
	Owned   int
	Serve   int
	// Recent counts unowned processes below staleProcessAge — seen, and
	// deliberately not reported.
	Recent  int
	Orphans []runningProcess
}

// classifyProcesses splits nightgauge processes into serve daemons, sidecar-
// owned runs, too-recent runs, and orphans. self is excluded: `doctor` is a
// nightgauge process and no sidecar claims it.
func classifyProcesses(procs []runningProcess, claimed map[int]bool, self int) processScan {
	var scan processScan
	for _, p := range procs {
		if !p.isNightgauge() || p.PID == self {
			continue
		}
		scan.Scanned++
		switch {
		case p.subcommand() == "serve":
			// Named exception. The extension host's serve daemon is long-lived
			// by design, owns no run, and writes no sidecar, so every other
			// rule here would report it on every invocation. #388 replaces this
			// argv test with a sidecar the daemon writes — until then the
			// exception is argv-shaped, which is the one place this file lets
			// argv decide anything.
			//
			// Residual ambiguity, accepted until #388 retires the argv test
			// entirely: subcommand() skips flags but cannot skip a flag's
			// VALUE, so `nightgauge --config serve …` would read `serve` as the
			// verb. It errs toward NOT reporting, which is the safe direction
			// for a check whose worst outcome is naming an operator's own
			// daemon.
			scan.Serve++
		case claimed[p.PID]:
			scan.Owned++
		case p.Age < staleProcessAge:
			scan.Recent++
		default:
			scan.Orphans = append(scan.Orphans, p)
		}
	}
	sort.Slice(scan.Orphans, func(i, j int) bool { return scan.Orphans[i].Age > scan.Orphans[j].Age })
	return scan
}

// checkOrphanedProcesses builds the doctor entry for running nightgauge
// processes no live sidecar claims (#341).
//
// now dates the sidecar CLAIMS, not the processes: a process's age comes from
// `ps`'s own etime column, the only clock that can date a process this scan did
// not start, but whether a sidecar's claim still counts is a question about
// wall-clock progress (see staleSidecarClaim).
func checkOrphanedProcesses(startDir string, now time.Time) (CheckItem, string) {
	raw, err := enumerateProcesses()
	if err != nil {
		return unverifiableProcessScan(err)
	}
	return processTableReport(raw, sidecarPIDs(startDir, now))
}

// processTableReport parses a raw `ps` table and reports on it, or explains why
// it could not. Split from checkOrphanedProcesses so every route out of a real
// table — parsed, unparsable, implausible — is reachable from a test without
// spawning a process.
func processTableReport(raw string, claimed map[int]bool) (CheckItem, string) {
	procs, determined := parseProcessTable(raw)
	if !determined {
		return unverifiableProcessScan(fmt.Errorf("`ps` output could not be parsed"))
	}
	// A table that parsed cleanly but does not list THIS process did not
	// really enumerate anything: `ps -ax` always includes its own caller, so a
	// self-less table (empty included) is a foreign or stubbed `ps` whose rows
	// this parser silently agreed with. Reporting "0 orphaned" from it would be
	// the #296 defect wearing a determined parse.
	if !listsPID(procs, os.Getpid()) {
		return unverifiableProcessScan(fmt.Errorf(
			"the parsed table has %d row(s) and does not include this process (pid %d), so it did not enumerate this machine",
			len(procs), os.Getpid()))
	}
	return orphanedProcessReport(procs, claimed)
}

// listsPID reports whether pid appears in the parsed table.
func listsPID(procs []runningProcess, pid int) bool {
	for _, p := range procs {
		if p.PID == pid {
			return true
		}
	}
	return false
}

// unverifiableProcessScan renders the house unverifiable outcome: never OK,
// never silent, and explicit that a clean report would be a claim about a scan
// that did not happen (#296, #323).
func unverifiableProcessScan(cause error) (CheckItem, string) {
	msg := fmt.Sprintf("orphaned processes unverifiable: could not enumerate running processes (%v) — no `ps` on this platform, or it failed, or its output was not the expected `pid etime command` shape. A clean report here would be an assertion about a scan that never ran", cause)
	return CheckItem{OK: false, Detail: "could not scan for orphaned nightgauge processes", Error: msg}, msg
}

// orphanedProcessReport turns a parsed table and the sidecar-claimed PID set
// into the check entry. Split from checkOrphanedProcesses so the reporting
// rules are testable against the captured process table.
func orphanedProcessReport(procs []runningProcess, claimed map[int]bool) (CheckItem, string) {
	scan := classifyProcesses(procs, claimed, os.Getpid())
	detail := fmt.Sprintf("%d nightgauge process(es): %d owned, %d serve, %d recent, %d orphaned",
		scan.Scanned, scan.Owned, scan.Serve, scan.Recent, len(scan.Orphans))
	if len(scan.Orphans) == 0 {
		return CheckItem{OK: true, Detail: detail}, ""
	}

	parts := make([]string, 0, maxLeaksReported+1)
	for i, p := range scan.Orphans {
		if i == maxLeaksReported {
			parts = append(parts, fmt.Sprintf("… and %d more", len(scan.Orphans)-maxLeaksReported))
			break
		}
		parts = append(parts, fmt.Sprintf("%d (%dh): %s", p.PID, int(p.Age.Hours()), p.Command))
	}
	msg := "orphaned nightgauge processes: " + strings.Join(parts, "; ") +
		" — no live sidecar claims these PIDs; verify and terminate manually"
	return CheckItem{OK: false, Detail: detail, Error: msg}, msg
}
