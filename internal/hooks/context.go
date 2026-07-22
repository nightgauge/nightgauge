package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ContextResult is the output of the context injection hook.
type ContextResult struct {
	Branch           string `json:"branch,omitempty"`
	IssueNumber      string `json:"issue_number,omitempty"`
	LastCommit       string `json:"last_commit,omitempty"`
	UncommittedCount int    `json:"uncommitted_changes,omitempty"`
	PlanProgress     string `json:"plan_progress,omitempty"`
	Message          string `json:"message,omitempty"`
}

// EvaluateContext gathers session context from the working directory.
// Used by SessionStart hooks to re-inject context on resume/compact.
func EvaluateContext(workdir string) ContextResult {
	result := ContextResult{}

	// Get current branch
	branch := getBranch(workdir)
	result.Branch = branch

	// Extract issue number from branch
	if branch != "" {
		matches := branchIssueNumber.FindStringSubmatch(branch)
		if len(matches) >= 2 {
			result.IssueNumber = matches[1]
		}
	}

	// Get last commit message
	result.LastCommit = getLastCommit(workdir)

	// Count uncommitted changes
	result.UncommittedCount = countUncommitted(workdir)

	// Get plan progress
	result.PlanProgress = getPlanProgress(workdir)

	// Build human-readable message
	result.Message = buildContextMessage(result)

	return result
}

// EvaluateContextJSON returns the context result as JSON bytes.
func EvaluateContextJSON(workdir string) ([]byte, error) {
	result := EvaluateContext(workdir)
	return json.Marshal(result)
}

// getBranch reads the current branch from .git/HEAD.
func getBranch(workdir string) string {
	headPath := filepath.Join(workdir, ".git", "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: refs/heads/") {
		return strings.TrimPrefix(head, "ref: refs/heads/")
	}
	return ""
}

// getLastCommit returns the last commit message subject line.
func getLastCommit(workdir string) string {
	cmd := exec.Command("git", "-C", workdir, "log", "-1", "--format=%s")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// countUncommitted returns the number of uncommitted files.
func countUncommitted(workdir string) int {
	cmd := exec.Command("git", "-C", workdir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
}

// getPlanProgress returns a human-readable plan completion string.
func getPlanProgress(workdir string) string {
	planFile := findPlanFile(workdir)
	if planFile == "" {
		return ""
	}

	status, err := parsePlanFile(planFile)
	if err != nil || status.Total == 0 {
		return ""
	}

	pct := float64(status.Complete) / float64(status.Total) * 100
	return fmt.Sprintf("%d/%d tasks (%.0f%%)", status.Complete, status.Total, pct)
}

// buildContextMessage creates a human-readable context summary.
func buildContextMessage(ctx ContextResult) string {
	var parts []string

	if ctx.Branch != "" {
		parts = append(parts, fmt.Sprintf("Branch: %s", ctx.Branch))
	}
	if ctx.IssueNumber != "" {
		parts = append(parts, fmt.Sprintf("Issue: #%s", ctx.IssueNumber))
	}
	if ctx.LastCommit != "" {
		parts = append(parts, fmt.Sprintf("Last commit: %s", ctx.LastCommit))
	}
	if ctx.UncommittedCount > 0 {
		parts = append(parts, fmt.Sprintf("Uncommitted changes: %d files", ctx.UncommittedCount))
	}
	if ctx.PlanProgress != "" {
		parts = append(parts, fmt.Sprintf("Plan progress: %s", ctx.PlanProgress))
	}

	if len(parts) == 0 {
		return "No context available"
	}

	return strings.Join(parts, "\n")
}
