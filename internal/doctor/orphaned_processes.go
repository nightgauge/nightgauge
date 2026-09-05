package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/nightgauge/nightgauge/internal/runstate"
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
// progress: the autonomous scheduler's state.json, the in-flight run's
// current-run.json and each attempt in run-state.json, across every sidecar
// root — plus every serve daemon on this MACHINE, from the claim directory
// eachServeClaim reads.
//
// The two halves have deliberately different reach, and the process table is
// why. `ps -axo` enumerates the whole box, so a claim store scoped to the
// invoking workspace can only narrow the processes that happen to belong to
// it; a serve daemon serving any other workspace would be reported as an
// orphan on every run (#388). Run and scheduler sidecars have no such problem
// — they name processes working inside the workspace that holds them.
//
// A claim counts only when the sidecar's own progress timestamp is within
// staleSidecarClaim of now — see that constant for why presence alone cannot
// be ownership. Each file is read with the timestamp its writer actually
// records: the scheduler's per-cycle lastScanAt, the run sidecar's
// stage_started_at, the serve daemon's 15-minute last_heartbeat_at,
// run-state's updated_at, each falling back to its start-of-life stamp when
// the progress field has not been written yet.
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

	// runstate.ServeSidecar: the daemon rewrites last_heartbeat_at every 15
	// minutes for as long as it is still attached to the host that started it,
	// so that stamp is serve's proof of life. Read by the SAME progress
	// doctrine as its peers and given no rule of its own — a serve-specific
	// exemption here would be the argv exception #388 retired, wearing a
	// filename.
	eachServeClaim(func(sc serveClaimRecord) {
		if progressIsFresh(now, sc.LastHeartbeatAt, sc.StartedAt) {
			claimPID(claimed, sc.PID)
		}
	})
	return claimed
}

// serveClaimRecord is one serve daemon's claim as this reader sees it.
//
// A minimal local struct on purpose, like every other sidecar shape in this
// file: doctor is a READER of records other packages own, and importing their
// types would make their schemas answerable to `doctor`.
type serveClaimRecord struct {
	PID             int    `json:"pid"`
	StartedAt       string `json:"started_at"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	WorkspaceRoot   string `json:"workspace_root"`
}

// eachServeClaim visits every serve claim on this machine (#388).
//
// Unconditional, and keyed to nothing about the invoking workspace: see
// sidecarPIDs for why a machine-wide process scan needs a machine-wide claim
// store. A missing directory (no daemon has ever run here) and a malformed
// file are both simply skipped — the same direction every unreadable sidecar
// fails in this file, toward UNOWNED and therefore toward REPORTING.
//
// The DIRECTORY WALK is runstate's, not this file's (#1426). It used to be a
// local ReadDir with a local `.json` suffix filter, which made this the second
// independent spelling of a layout runstate owns — the hazard the comment
// above already named for the path alone, and the registry now has a lock
// suffix, a temp-file shape and a reversible key for a second walker to
// disagree about too. What stays local is the record SHAPE, which is the part
// doctor is genuinely entitled to its own opinion of.
func eachServeClaim(visit func(serveClaimRecord)) {
	runstate.EachServeRegistryFile(func(f runstate.ServeRegistryFile) {
		if f.Lock || len(f.Data) == 0 {
			return
		}
		var sc serveClaimRecord
		if json.Unmarshal(f.Data, &sc) != nil {
			return
		}
		visit(sc)
	})
}

// staleServeClaims maps the PID of every serve claim that has STOPPED making
// progress to the workspace root that claim named.
//
// This is the shape #388 exists to surface: a daemon that outlived its host
// stops refreshing (runstate.serveClaim.tick) and its record goes cold, so when
// a still-running process matches one of these PIDs, this is what tells the
// operator WHICH workspace's daemon they are looking at. Evidence, never
// ownership: a recycled PID
// would be attributed to the wrong process, the same accepted edge as every
// other claim here, which is why it only ever decorates a line the report was
// already going to print.
func staleServeClaims(now time.Time) map[int]string {
	stale := map[int]string{}
	eachServeClaim(func(sc serveClaimRecord) {
		if sc.PID > 0 && sc.WorkspaceRoot != "" && !progressIsFresh(now, sc.LastHeartbeatAt, sc.StartedAt) {
			stale[sc.PID] = sc.WorkspaceRoot
		}
	})
	return stale
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
	// Recent counts unowned processes below staleProcessAge — seen, and
	// deliberately not reported.
	Recent  int
	Orphans []runningProcess
}

// classifyProcesses splits nightgauge processes into sidecar-owned runs,
// too-recent runs, and orphans. self is excluded: `doctor` is a nightgauge
// process and no sidecar claims it.
//
// There is no verb-shaped arm here and there must not be one. `serve` had a
// named argv exception until #388 gave the daemon a heartbeat sidecar: it was
// the one place this file let argv decide ownership, and what it actually
// bought was invisibility — a serve daemon that outlived its extension host
// was excepted exactly like a healthy one, which is the symptom this whole
// carrier exists to surface. Every process is now claimed by a sidecar or
// reported.
func classifyProcesses(procs []runningProcess, claimed map[int]bool, self int) processScan {
	var scan processScan
	for _, p := range procs {
		if !p.isNightgauge() || p.PID == self {
			continue
		}
		scan.Scanned++
		switch {
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
	return processTableReport(startDir, raw, sidecarPIDs(startDir, now), staleServeClaims(now))
}

// processTableReport parses a raw `ps` table and reports on it, or explains why
// it could not. Split from checkOrphanedProcesses so every route out of a real
// table — parsed, unparsable, implausible — is reachable from a test without
// spawning a process.
func processTableReport(startDir, raw string, claimed map[int]bool, staleServe map[int]string) (CheckItem, string) {
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
	fc, cwdScanOK := buildForeignCwdScan(startDir, procs)
	if !cwdScanOK {
		// The mechanism itself did not work — `git worktree list` failed, or
		// the cwd source (lsof/proc) could not be read. That is a scan that
		// never ran, not a scan that found nothing, and the two must not
		// collapse into the same "OK" the process-table half already guards
		// against above (#296). Distinct from the len(repoRoots)==0 case
		// inside buildForeignCwdScan, which legitimately has nothing to say.
		return unverifiableProcessScan(fmt.Errorf(
			"the process table parsed, but the cwd-inside-worktree half could not run: `git worktree list` failed, or the process cwd source (lsof/proc) was unavailable"))
	}
	return orphanedProcessReport(procs, claimed, staleServe, fc)
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
//
// staleServe attributes a reported PID to the workspace whose serve claim went
// cold on it (see staleServeClaims). It only ever adds a clause to a line that
// was already going to be printed — an orphan is an orphan whether or not this
// map knows anything about it.
//
// fc is the second, unrelated half added by #519: foreign (non-nightgauge)
// processes whose cwd sits inside a pipeline worktree. nil means that half did
// not run (no worktrees root, or the cwd source was unavailable) and the
// report says nothing about it — the same "this half answers nothing" shape
// classifyProcesses already has for an empty claimed map.
func orphanedProcessReport(procs []runningProcess, claimed map[int]bool, staleServe map[int]string, fc *foreignCwdScan) (CheckItem, string) {
	scan := classifyProcesses(procs, claimed, os.Getpid())
	var holders []foreignCwdHolder
	if fc != nil {
		holders = classifyForeignCwdHolders(procs, fc.Cwds, fc.RepoRoots, fc.ActiveByRepo, os.Getpid())
	}
	detail := fmt.Sprintf("%d nightgauge process(es): %d owned, %d recent, %d orphaned",
		scan.Scanned, scan.Owned, scan.Recent, len(scan.Orphans))
	if len(holders) > 0 {
		detail += fmt.Sprintf(", %d with cwd inside a worktree", len(holders))
	}
	if len(scan.Orphans) == 0 && len(holders) == 0 {
		return CheckItem{OK: true, Detail: detail}, ""
	}

	var msg string
	if len(scan.Orphans) > 0 {
		parts := make([]string, 0, maxLeaksReported+1)
		for i, p := range scan.Orphans {
			if i == maxLeaksReported {
				parts = append(parts, fmt.Sprintf("… and %d more", len(scan.Orphans)-maxLeaksReported))
				break
			}
			part := fmt.Sprintf("%d (%dh): %s", p.PID, int(p.Age.Hours()), p.Command)
			if ws := staleServe[p.PID]; ws != "" {
				part += fmt.Sprintf(" [its serve claim for %s stopped making progress]", ws)
			}
			parts = append(parts, part)
		}
		msg = "orphaned nightgauge processes: " + strings.Join(parts, "; ") +
			" — no live sidecar claims these PIDs; verify and terminate manually"
	}
	if len(holders) > 0 {
		parts := make([]string, 0, maxLeaksReported+1)
		for i, h := range holders {
			if i == maxLeaksReported {
				parts = append(parts, fmt.Sprintf("… and %d more", len(holders)-maxLeaksReported))
				break
			}
			tag := fmt.Sprintf("cwd inside worktree issue-%d", h.IssueNumber)
			if h.Stale {
				tag = fmt.Sprintf("cwd inside REMOVED worktree issue-%d", h.IssueNumber)
			}
			parts = append(parts, fmt.Sprintf("%d (%dh): %s (cwd %s) [%s]", h.PID, int(h.Age.Hours()), h.Command, h.Cwd, tag))
		}
		fcMsg := "processes with cwd inside a pipeline worktree: " + strings.Join(parts, "; ") +
			" — verify and terminate manually"
		if msg != "" {
			msg += "; " + fcMsg
		} else {
			msg = fcMsg
		}
	}
	return CheckItem{OK: false, Detail: detail, Error: msg}, msg
}

// --- Foreign cwd holders (#519) ---
//
// #341's scan only ever looked for processes the PIPELINE spawned. But a
// `.nightgauge/worktrees/issue-N` directory is also where interactive agent
// harnesses (Claude Code, Codex, both inside VSCode) run their shells, and
// those harnesses can leak detached ones: an operator found several `/bin/zsh`
// processes still parked with cwd inside `.nightgauge/worktrees/issue-488`,
// held open by a VSCode extension-host background task long after the session
// ended and the worktree itself was removed. None of those shells was ever a
// nightgauge process, so #341's argv-based filter could never have seen them —
// this half of the scan is keyed on cwd instead of parentage, and runs over
// EVERY row, nightgauge or not.
//
// Report-only, same as #341: this half only ever reads `ps`, `lsof`/`/proc`
// and `git worktree list` — it names PIDs for an operator to verify and kill
// by hand, and never signals one itself.

// foreignCwdScan bundles the OS- and git-derived facts orphanedProcessReport
// needs to run the cwd half of the scan. A nil pointer means there is
// genuinely nothing to scan — no repo root in this workspace has ever held a
// pipeline worktree — which is distinct from the mechanism failing; see
// buildForeignCwdScan's second return value for that case.
//
// ActiveByRepo is keyed per REPO ROOT, not merged into one issue-number set
// across the workspace: two repos can legally both be mid-flight on the same
// issue number (each repo's issue numbers are its own), so a live worktree
// for issue-488 in repo B must never mark a holder sitting in repo A's
// already-removed issue-488 worktree as live (#519 review finding).
type foreignCwdScan struct {
	Cwds         map[int]string
	RepoRoots    []string
	ActiveByRepo map[string]map[int]bool
}

// foreignCwdHolder is one process — not necessarily a nightgauge one — whose
// cwd resolves inside a pipeline worktree.
type foreignCwdHolder struct {
	PID         int
	Age         time.Duration
	Command     string
	Cwd         string
	IssueNumber int
	// Stale is true when the worktree this cwd sits inside no longer appears
	// in `git worktree list` — the process is holding open a directory the
	// pipeline itself considers gone, which is the shape most worth an
	// operator's attention: it can also block `git worktree remove` (#110).
	Stale bool
}

// cwdTimeout bounds the cwd lookup so a wedged `lsof`/`/proc` read cannot hang
// `doctor`, the same discipline psTimeout applies to `ps` itself.
const cwdTimeout = 10 * time.Second

// worktreeBaseDirs is every directory name, relative to a repo root, this
// scan treats as a pipeline worktree base. There are three live layouts on a
// real machine, not one: the Go execution.Manager's own default
// (.nightgauge/worktrees, execution/worktree.go's ensureWorktree), the VSCode
// extension's default (.worktrees, WorktreeManager.ts) — the interactive
// harness whose leaked shells motivated #519 in the first place — and Claude
// Code's own worktree base (.claude/worktrees). See
// docs/GO_BINARY.md's worktree-layout table and
// worktreeContainment.ts's isLinkedWorktree doc comment, which independently
// names the same three.
//
// Best-effort, not authoritative: `pipeline.worktree_base` lets an operator
// configure a fourth location, and that setting is TypeScript-side config
// this Go binary does not parse — a custom base is invisible to this scan.
// Deliberately narrower than execution.ActiveWorktreeIssues' own notion of "a
// worktree": that answers "what has git registered?" anywhere on disk, which
// is right for reclaiming stray worktrees a maintainer hand-created
// elsewhere, but wrong here — a process with cwd in some unrelated hand-made
// worktree is not evidence of the interactive-harness leak this half exists
// to catch.
var worktreeBaseDirs = []string{
	filepath.Join(".nightgauge", "worktrees"),
	".worktrees",
	filepath.Join(".claude", "worktrees"),
}

// buildForeignCwdScan gathers the cwd-half inputs for this run. The second
// return value is false only when the MECHANISM did not work — `git worktree
// list` failed in some repo root, or the cwd source itself could not be read
// (unsupported platform, `lsof`/`/proc` unreachable) — which the caller must
// route through unverifiableProcessScan rather than silently reporting clean
// (#296). A workspace with no repo root at all has genuinely nothing to scan;
// that is the one case that legitimately returns (nil, true).
//
// Active worktrees are resolved ONE REPO ROOT AT A TIME and kept in that
// shape (foreignCwdScan.ActiveByRepo) rather than merged into one
// workspace-wide issue-number set: two repos can each have their own
// issue-488, live or removed independently, and merging would let one repo's
// live worktree paper over another repo's removed one.
func buildForeignCwdScan(startDir string, procs []runningProcess) (*foreignCwdScan, bool) {
	repoRoots := config.WorkspaceRepoRoots(startDir)
	if len(repoRoots) == 0 {
		return nil, true
	}
	activeByRepo := make(map[string]map[int]bool, len(repoRoots))
	for _, root := range repoRoots {
		active, determined := execution.ActiveWorktreeIssues([]string{root})
		if !determined {
			return nil, false
		}
		activeByRepo[root] = active
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	cwds, determined := lookupCwds(pids)
	if !determined {
		return nil, false
	}
	return &foreignCwdScan{Cwds: cwds, RepoRoots: repoRoots, ActiveByRepo: activeByRepo}, true
}

// classifyForeignCwdHolders finds every process (self excluded) whose cwd
// resolves inside one of repoRoots' worktree bases, tagging each as holding a
// live or a REMOVED worktree.
//
// Runs over ALL rows, unlike classifyProcesses — the whole point of this half
// is the processes #341's isNightgauge() filter can never see. self is still
// excluded: a stage legitimately runs `doctor` FROM inside its own worktree,
// and that is not a leak.
//
// A LIVE worktree is exactly where every pipeline stage and every interactive
// agent session legitimately runs (execution.Manager sets cmd.Dir to it,
// and so does every harness worktreeBaseDirs names) — flagging one the
// instant it starts would tell an operator to "verify and terminate" work
// that is currently running. staleProcessAge gates live holders the same way
// it gates #341's own half; a REMOVED worktree has no legitimate occupant at
// any age, so it is reported regardless — that is the headline #519 incident.
func classifyForeignCwdHolders(procs []runningProcess, cwds map[int]string, repoRoots []string, activeByRepo map[string]map[int]bool, self int) []foreignCwdHolder {
	var holders []foreignCwdHolder
	for _, p := range procs {
		if p.PID == self {
			continue
		}
		cwd, ok := cwds[p.PID]
		if !ok || cwd == "" {
			continue
		}
		repoRoot, base, ok := worktreeDirContaining(cwd, repoRoots)
		if !ok {
			continue
		}
		issueNum, ok := execution.IssueNumberFromWorktreeDir(base)
		if !ok {
			continue
		}
		stale := !activeByRepo[repoRoot][issueNum]
		if !stale && p.Age < staleProcessAge {
			continue
		}
		holders = append(holders, foreignCwdHolder{
			PID: p.PID, Age: p.Age, Command: p.Command, Cwd: cwd,
			IssueNumber: issueNum, Stale: stale,
		})
	}
	// Stale-first (the case that can also block a worktree removal, #110), and
	// within each group the same oldest-first order classifyProcesses uses.
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].Stale != holders[j].Stale {
			return holders[i].Stale
		}
		return holders[i].Age > holders[j].Age
	})
	return holders
}

// worktreeDirContaining reports whether cwd resolves inside one of repoRoots'
// known worktree bases (worktreeBaseDirs) and, if so, which repo root matched
// and the immediate child directory name — the worktree's own basename (e.g.
// "issue-488" or "myrepo-issue-488").
//
// cwd is deliberately NOT required to exist: `git worktree remove` deletes
// the directory outright, and a shell still parked there — the headline #519
// incident — must still be found. cwd is only Clean'd and made absolute
// (lexically, never filepath.EvalSymlinks, which fails ENOENT on a removed
// directory); only the worktree-base side is resolved with EvalSymlinks
// (macOS's /tmp → /private/tmp is the routine case there), because that
// parent directory — unlike the specific worktree subdirectory that may have
// been removed — still exists whenever this scan has anything to find.
// Containment is decided by filepath.Rel, never a string prefix:
// "…/worktrees-old/x" shares the literal prefix "…/worktrees" with
// "…/worktrees/x" but is Rel-relative "../worktrees-old/x", which is
// rejected. A base directory that was never created (this repo never used
// that harness) simply fails EvalSymlinks and is skipped, not fatal to the
// other bases or repo roots.
func worktreeDirContaining(cwd string, repoRoots []string) (repoRoot, base string, ok bool) {
	clean := filepath.Clean(cwd)
	if !filepath.IsAbs(clean) {
		abs, err := filepath.Abs(clean)
		if err != nil {
			return "", "", false
		}
		clean = abs
	}
	for _, root := range repoRoots {
		for _, baseDir := range worktreeBaseDirs {
			rr, err := filepath.EvalSymlinks(filepath.Join(root, baseDir))
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(rr, clean)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
				rel = rel[:i]
			}
			return root, rel, true
		}
	}
	return "", "", false
}

// lookupCwds resolves the cwd of every pid in ONE bounded call per platform —
// never one invocation per process, which would make a machine-wide scan
// spawn a subprocess per row — and reports whether the answer is DETERMINED.
// determined=false means the mechanism itself did not work (unsupported
// platform, the command timed out or produced nothing at all); a specific pid
// simply missing from the returned map (permission denied on someone else's
// process, or it exited mid-scan) is routine and does NOT undetermine the
// whole lookup — the same "one gap does not undetermine an unrelated
// narrowing filter" doctrine sidecarPIDs already applies to an unreadable
// sidecar.
func lookupCwds(pids []int) (map[int]string, bool) {
	if len(pids) == 0 {
		return map[int]string{}, true
	}
	switch runtime.GOOS {
	case "darwin":
		return lookupCwdsDarwin(pids)
	case "linux":
		return lookupCwdsLinux(pids), true
	default:
		return nil, false
	}
}

// lookupCwdsLinux reads /proc/<pid>/cwd directly — no subprocess, no timeout
// needed. A pid this reader cannot resolve (already exited, owned by another
// user) is simply absent from the result.
//
// The kernel appends " (deleted)" to the readlink target when the directory
// itself no longer exists — exactly the #519 headline case, a process still
// parked in a worktree `git worktree remove` deleted out from under it — and
// that suffix is stripped here, at the source, so every downstream consumer
// (worktreeDirContaining above all) sees a plain path rather than needing its
// own copy of this platform-specific shape.
func lookupCwdsLinux(pids []int) map[int]string {
	cwds := make(map[int]string, len(pids))
	for _, pid := range pids {
		if link, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
			cwds[pid] = strings.TrimSuffix(link, " (deleted)")
		}
	}
	return cwds
}

// lookupCwdsDarwin shells `lsof -a -d cwd -Fpn -p <pid list>` ONCE for every
// pid this scan cares about. `lsof` exits non-zero whenever ANY requested pid
// could not be inspected (already exited, owned by another user) — routine on
// a machine-wide scan and not a failure of the mechanism itself; only the
// absence of ANY usable output (the binary is missing, every pid was
// inaccessible, the timeout fired) means the mechanism did not work.
func lookupCwdsDarwin(pids []int) (map[int]string, bool) {
	strs := make([]string, len(pids))
	for i, p := range pids {
		strs[i] = strconv.Itoa(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cwdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(strs, ",")).Output()
	if len(out) == 0 {
		if err != nil {
			return nil, false
		}
		return map[int]string{}, true
	}
	cwds, _ := parseLsofCwd(string(out))
	return cwds, true
}

// parseLsofCwd parses `lsof -Fpn` output into pid → cwd. The format is
// repeating `p<pid>` blocks; a `cwd`-filtered block ("-d cwd") also carries an
// `f` (file-descriptor) line this scan does not need, so every field but `p`
// and `n` is read and ignored rather than assumed absent. A block whose
// shape this parser does not recognize (an `n` line with no preceding `p`, an
// unparsable pid) is skipped and logged, not fatal to the rest of the table —
// the malformed block is the one entry lost, not the whole scan.
func parseLsofCwd(raw string) (map[int]string, bool) {
	cwds := map[int]string{}
	curPID, havePID := 0, false
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		tag, val := line[0], line[1:]
		switch tag {
		case 'p':
			pid, err := strconv.Atoi(val)
			if err != nil || pid <= 0 {
				log.Printf("orphaned-processes: lsof -Fpn: malformed pid line %q — skipped", line)
				havePID = false
				continue
			}
			curPID, havePID = pid, true
		case 'n':
			if !havePID {
				log.Printf("orphaned-processes: lsof -Fpn: %q with no preceding pid — skipped", line)
				continue
			}
			cwds[curPID] = val
		default:
			// 'f' (the cwd file descriptor) and anything else lsof emits:
			// evidence this parser does not need.
		}
	}
	return cwds, true
}
