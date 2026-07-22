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
}

var (
	// checkboxComplete matches "- [x] ..." or "- [X] ..."
	checkboxComplete = regexp.MustCompile(`^\s*-\s+\[x\]\s`)
	// checkboxIncomplete matches "- [ ] ..."
	checkboxIncomplete = regexp.MustCompile(`^\s*-\s+\[ \]\s`)
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
		// and runs recoverUncommittedWork() to preserve the work.
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
	// recoverUncommittedWork path; this stdout block is purely for the agent.
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

// parsePlanFile reads a plan file and counts checkboxes.
func parsePlanFile(path string) (*PlanStatus, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	status := &PlanStatus{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if checkboxComplete.MatchString(line) {
			status.Complete++
			status.Total++
		} else if checkboxIncomplete.MatchString(line) {
			status.Incomplete++
			status.Total++
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
