package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// ContextResult is the output of the context injection hook.
type ContextResult struct {
	Branch           string `json:"branch,omitempty"`
	IssueNumber      string `json:"issue_number,omitempty"`
	LastCommit       string `json:"last_commit,omitempty"`
	UncommittedCount int    `json:"uncommitted_changes,omitempty"`
	PlanProgress     string `json:"plan_progress,omitempty"`
	// StageArtifacts are the issue's pipeline handoff files already on disk,
	// as absolute paths in the clone's pipeline state and plans directories
	// (ADR-024 § 7). Injected on compaction so a multi-phase stage resumes
	// after its last finished phase instead of starting over: a local model
	// whose planning stage compacted at phase 12 went back to phase 5 and
	// rewrote a plan it had already written.
	StageArtifacts []string `json:"stage_artifacts,omitempty"`
	Message        string   `json:"message,omitempty"`
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

	result.StageArtifacts = stageArtifacts(workdir, result.IssueNumber)

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

// stageArtifacts lists the issue's pipeline handoff files present in the
// clone's pipeline state and plans directories, in pipeline order. The paths
// are absolute: those directories live under the git common dir (ADR-024 § 7),
// which from a linked worktree is not beneath workdir.
func stageArtifacts(workdir, issue string) []string {
	if issue == "" {
		return nil
	}
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return nil
	}
	var found []string
	if pipelineDir, err := layout.PipelineStateDir(absWorkdir); err == nil {
		for _, name := range []string{"issue", "ac-reconcile", "planning", "dev", "validate", "pr"} {
			path := filepath.Join(pipelineDir, name+"-"+issue+".json")
			if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
				found = append(found, path)
			}
		}
	}
	if plansDir, err := layout.PlansDir(absWorkdir); err == nil {
		plans, _ := filepath.Glob(filepath.Join(plansDir, issue+"-*.md"))
		found = append(found, plans...)
	}
	return found
}

// cloneDir resolves one per-clone class directory through its layout
// resolver (layout.PipelineStateDir, layout.PlansDir, ...). A relative root is
// made absolute first, so it names the same directory as before.
func cloneDir(resolve func(string) (string, error), root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	return resolve(abs)
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
	if len(ctx.StageArtifacts) > 0 {
		parts = append(parts, fmt.Sprintf("Already written: %s. Resume after the last phase whose output is here; read these files rather than redoing the phases that produced them.",
			strings.Join(ctx.StageArtifacts, ", ")))
	}

	if len(parts) == 0 {
		return "No context available"
	}

	return strings.Join(parts, "\n")
}
