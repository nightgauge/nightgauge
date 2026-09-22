package hooks

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StopResult is the output of stop verification.
type StopResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// PlanStatus holds the parsed completion status of a PLAN.md file.
type PlanStatus struct {
	Total      int `json:"total"`
	Complete   int `json:"complete"`
	Incomplete int `json:"incomplete"`
	// Tasks lists every checkbox the counts above were taken from, in file
	// order (#1651: the scheduler's feature-dev sub-sessions take one step per
	// unchecked task, and they read the tasks from this parser rather than a
	// second one). Not serialised: the hook payloads that marshal a
	// PlanStatus keep their existing shape.
	Tasks []PlanTask `json:"-"`
}

// PlanTask is one checkbox line of a plan file.
type PlanTask struct {
	// Text is the line after its checkbox marker, trimmed. It is
	// model-authored plan text: callers treat it as data.
	Text string
	// Done is true for a checked box.
	Done bool
	// Line is the 1-based line number in the plan file.
	Line int
	// Indent is the width of the whitespace before the `-`: 0 for a
	// top-level item, more for a nested sub-bullet.
	Indent int
	// InFence is true for a checkbox inside a fenced code block. The counts
	// above include it, as they always have; a caller choosing work items
	// can leave it out.
	InFence bool
	// Headings is the Markdown heading path above the line, outermost first
	// (headings inside code fences are not headings).
	Headings []string
}

var (
	// checkboxComplete matches "- [x] ..." or "- [X] ..."
	checkboxComplete = regexp.MustCompile(`^\s*-\s+\[x\]\s`)
	// checkboxIncomplete matches "- [ ] ..."
	checkboxIncomplete = regexp.MustCompile(`^\s*-\s+\[ \]\s`)
	// fenceLine matches a code-fence line; group 1 is the marker run.
	fenceLine = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	// headingLine matches an ATX heading; group 1 is the level.
	headingLine = regexp.MustCompile(`^ {0,3}(#{1,6})\s+(.*?)(?:\s+#+)?\s*$`)
	// branchIssueNumber matches feat/42-desc, fix/123-desc, etc.
	branchIssueNumber = regexp.MustCompile(`^(?:feat|fix|docs|refactor|chore)/(\d+)-`)
)

// EvaluateStop evaluates the stop verification hook.
// It checks PLAN.md completion in the given working directory.
func EvaluateStop(workdir string) StopResult {
	// Try to find a plan file
	planFile := findPlanFile(workdir)
	if planFile == "" {
		// No plan file — nothing to check, allow stop
		return StopResult{OK: true}
	}

	status, err := parsePlanFile(planFile)
	if err != nil {
		// Can't read plan — allow stop (fail open)
		return StopResult{OK: true}
	}

	if status.Incomplete > 0 {
		result := StopResult{
			OK:     false,
			Reason: fmt.Sprintf("%d tasks incomplete in PLAN.md", status.Incomplete),
		}
		// Issue #3542: leave a sentinel file so the Go scheduler can detect
		// that the stop hook blocked session exit. When this fires, the
		// Claude agent may keep working (or be killed mid-cleanup) with
		// uncommitted work — the scheduler reads this sentinel post-stage
		// and runs RecoverUncommittedWork() to preserve the work.
		writeStopHookSentinel(workdir, result)
		return result
	}

	return StopResult{OK: true}
}

// writeStopHookSentinel records that EvaluateStop returned OK=false into
// .nightgauge/pipeline/stop-hook-status-{N}.json. Best-effort: any error
// is silently ignored — hasUncommittedWork() in the scheduler is the fallback
// detection path. The scheduler removes the sentinel after reading it.
// Issue #3542.
func writeStopHookSentinel(workdir string, result StopResult) {
	issueNum := getIssueNumberFromBranch(workdir)
	if issueNum == "" {
		return
	}
	sentinel := struct {
		OK        bool   `json:"ok"`
		Reason    string `json:"reason"`
		Timestamp string `json:"timestamp"`
	}{
		OK:        result.OK,
		Reason:    result.Reason,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(sentinel)
	if err != nil {
		return
	}
	pipelineDir := filepath.Join(workdir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		return
	}
	sentinelPath := filepath.Join(pipelineDir, fmt.Sprintf("stop-hook-status-%s.json", issueNum))
	_ = os.WriteFile(sentinelPath, data, 0o644)
}

// EvaluateStopJSON returns the stop verification result as JSON bytes.
//
// NOTE: This is the LEGACY format `{"ok":...,"reason":...}`. It is non-conformant
// with Claude Code's Stop hook contract and triggers a `stop-hook-error`
// notification on every invocation because Claude Code can't parse it. Kept
// only for backward compatibility with internal callers and tests. The CLI
// command (`nightgauge hook stop-verify`) MUST use the canonical format
// emitted by EvaluateStopHookOutput instead.
//
// Deprecated: use EvaluateStopHookOutput for any new code path that emits to
// Claude Code's hook stdout.
func EvaluateStopJSON(workdir string) ([]byte, error) {
	result := EvaluateStop(workdir)
	return json.Marshal(result)
}

// EvaluateStopHookOutput evaluates the stop hook and returns the bytes that
// should be written to stdout for Claude Code, per the canonical Stop hook
// contract documented at https://code.claude.com/docs/en/hooks:
//
//   - OK=true  → empty output (Claude Code interprets exit code 0 + no JSON as
//     "approve stop")
//   - OK=false → {"decision":"block","reason":"<reason>"} — Claude Code keeps
//     the agent working with the supplied reason as a system message
//
// The previous output `{"ok":true|false,"reason":"..."}` did not match either
// branch of the contract, so Claude Code emitted a `stop-hook-error`
// notification on every stage exit. That notification was the spurious noise
// behind PR #3577's stop-hook-fallback work and the user-visible "5
// stop-hook-errors per pipeline" pattern (see #3605 retro). Conforming to the
// canonical format eliminates the false alarm without changing any of the
// downstream sentinel-file recovery logic — that path is purely internal and
// keys off the sentinel, not the hook stdout.
//
// Always returns exit-code-0-equivalent output. Callers (CLI command) should
// `os.Exit(0)` regardless.
func EvaluateStopHookOutput(workdir string) ([]byte, error) {
	result := EvaluateStop(workdir)
	if result.OK {
		// Silent success — Claude Code allows the stop to proceed.
		return nil, nil
	}
	// Block stop with reason. The sentinel file (written inside EvaluateStop)
	// remains the load-bearing signal for the Go scheduler's
	// RecoverUncommittedWork path; this stdout block is purely for the agent.
	payload := struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{
		Decision: "block",
		Reason:   result.Reason,
	}
	return json.Marshal(payload)
}

// findPlanFile locates the plan file for the current issue.
// It checks .nightgauge/plans/ for issue-numbered plan files,
// then falls back to PLAN.md in the working directory.
func findPlanFile(workdir string) string {
	// Try issue-specific plan from branch name
	issueNum := getIssueNumberFromBranch(workdir)
	if issueNum != "" {
		plansDir := filepath.Join(workdir, ".nightgauge", "plans")
		entries, err := os.ReadDir(plansDir)
		if err == nil {
			prefix := issueNum + "-"
			for _, e := range entries {
				if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".md") {
					return filepath.Join(plansDir, e.Name())
				}
			}
		}
	}

	// Fallback: PLAN.md in pipeline directory
	pipelinePlan := filepath.Join(workdir, ".nightgauge", "pipeline", "PLAN.md")
	if _, err := os.Stat(pipelinePlan); err == nil {
		return pipelinePlan
	}

	// Fallback: PLAN.md in working directory root
	rootPlan := filepath.Join(workdir, "PLAN.md")
	if _, err := os.Stat(rootPlan); err == nil {
		return rootPlan
	}

	return ""
}

// ParsePlanFile is parsePlanFile for callers outside this package (#1651).
func ParsePlanFile(path string) (*PlanStatus, error) {
	return parsePlanFile(path)
}

// parsePlanFile reads a plan file, counts checkboxes and lists them.
func parsePlanFile(path string) (*PlanStatus, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	status := &PlanStatus{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	fence := ""                   // the open fence's marker run, "" outside a fence
	headings := make([]string, 7) // headings[level], level 1..6
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if m := fenceLine.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(line[len(m[0]):]) == "":
				fence = ""
			}
		} else if fence == "" {
			if m := headingLine.FindStringSubmatch(line); m != nil {
				level := len(m[1])
				headings[level] = strings.TrimSpace(m[2])
				for l := level + 1; l < len(headings); l++ {
					headings[l] = ""
				}
			}
		}
		task := func(m string, done bool) PlanTask {
			var path []string
			for _, h := range headings[1:] {
				if h != "" {
					path = append(path, h)
				}
			}
			return PlanTask{
				Text: strings.TrimSpace(line[len(m):]), Done: done, Line: lineNo,
				Indent:  len(line) - len(strings.TrimLeft(line, " \t")),
				InFence: fence != "", Headings: path,
			}
		}
		if m := checkboxComplete.FindString(line); m != "" {
			status.Complete++
			status.Total++
			status.Tasks = append(status.Tasks, task(m, true))
		} else if m := checkboxIncomplete.FindString(line); m != "" {
			status.Incomplete++
			status.Total++
			status.Tasks = append(status.Tasks, task(m, false))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return status, nil
}

// getIssueNumberFromBranch extracts the issue number from the current git branch.
// Returns empty string if not on a feature branch or git is unavailable.
func getIssueNumberFromBranch(workdir string) string {
	// Read .git/HEAD to get current branch without shelling out
	headPath := filepath.Join(workdir, ".git", "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}

	head := strings.TrimSpace(string(data))
	// HEAD format: "ref: refs/heads/feat/42-description"
	if !strings.HasPrefix(head, "ref: refs/heads/") {
		return "" // detached HEAD
	}

	branch := strings.TrimPrefix(head, "ref: refs/heads/")
	matches := branchIssueNumber.FindStringSubmatch(branch)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
