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
)

// Leaked processes — nightgauge processes still running that no live sidecar
// claims (#341).
//
// Same family as the worktree and stash carriers next door, one layer down: a
// stage that is killed leaks its worktree, but a stage that is never killed
// leaks ITSELF. `nightgauge autonomous run --dry-run` sat on this machine for
// 31 hours holding a scheduler slot, and every one of doctor's twelve checks
// passed the whole time — none of them enumerated processes, so the leak had
// no reporter at all.
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
func (p runningProcess) isNightgauge() bool {
	exe := p.firstToken()
	return exe != "" && filepath.Base(exe) == "nightgauge"
}

// subcommand returns argv[1], or "" when the process was invoked bare.
func (p runningProcess) subcommand() string {
	fields := strings.Fields(p.Command)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
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

// sidecarPIDs collects every PID claimed by a live run's on-disk sidecar across
// the workspace's repo roots: the autonomous scheduler's state.json, the
// in-flight run's current-run.json, and each attempt in run-state.json.
//
// A missing or unparsable sidecar contributes no PIDs and does NOT undetermine
// the scan. Ownership is a narrowing filter here, so failing to read one fails
// toward UNOWNED, which fails toward REPORTING — safe for a report-only carrier
// in a way it would not be for anything that acted on the answer.
//
// Accepted edge: a dead writer's PID recycled by another nightgauge process
// reads as OWNED, and that process would go unreported. Deliberately not
// mitigated — the alternative is probing writer liveness per sidecar, which
// buys a rare false negative at the cost of a second liveness authority.
func sidecarPIDs(startDir string) map[int]bool {
	claimed := map[int]bool{}
	for _, root := range config.WorkspaceRepoRoots(startDir) {
		var autonomous struct {
			PID int `json:"pid"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "autonomous", "state.json"), &autonomous) {
			claimPID(claimed, autonomous.PID)
		}

		var currentRun struct {
			PID int `json:"pid"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "pipeline", "current-run.json"), &currentRun) {
			claimPID(claimed, currentRun.PID)
		}

		var runState struct {
			Attempts []struct {
				PID *int `json:"pid"`
			} `json:"attempts"`
		}
		if readJSONFile(filepath.Join(root, ".nightgauge", "pipeline", "run-state.json"), &runState) {
			for _, a := range runState.Attempts {
				if a.PID != nil {
					claimPID(claimed, *a.PID)
				}
			}
		}
	}
	return claimed
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
// now is unused: a process's age comes from `ps`'s own etime column, which is
// the only clock that can date a process this scan did not start. The parameter
// is kept so every leak carrier in this package has one signature.
func checkOrphanedProcesses(startDir string, now time.Time) (CheckItem, string) {
	_ = now
	raw, err := enumerateProcesses()
	if err != nil {
		return unverifiableProcessScan(err)
	}
	procs, determined := parseProcessTable(raw)
	if !determined {
		return unverifiableProcessScan(fmt.Errorf("`ps` output could not be parsed"))
	}
	return orphanedProcessReport(procs, sidecarPIDs(startDir))
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
