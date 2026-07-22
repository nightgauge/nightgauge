package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PrePushInput holds parameters for the pre-push validation gate.
type PrePushInput struct {
	IssueNumber   int
	WorkDir       string
	TargetBranch  string
	FeatureBranch string
}

// PrePushResult holds the outcome of pre-push validation.
type PrePushResult struct {
	Decision         string            `json:"decision"` // "allow" or "block"
	IssueNumber      int               `json:"issue_number"`
	TargetBranch     string            `json:"target_branch"`
	FeatureBranch    string            `json:"feature_branch"`
	ValidationPhases map[string]string `json:"validation_phases"` // phase -> "passed"|"failed"|"skipped"
	CriticalFindings int               `json:"critical_findings"`
	Reason           string            `json:"reason,omitempty"`
	ContextPath      string            `json:"context_path"`
	StartedAt        string            `json:"started_at"`
	CompletedAt      string            `json:"completed_at"`
}

// PrePushContextFile represents the pre-push-{N}.json pipeline context file.
type PrePushContextFile struct {
	SchemaVersion    string            `json:"schema_version"`
	IssueNumber      int               `json:"issue_number"`
	TargetBranch     string            `json:"target_branch"`
	FeatureBranch    string            `json:"feature_branch"`
	OverallStatus    string            `json:"overall_status"` // "passed" or "failed"
	ValidationPhases map[string]string `json:"validation_phases"`
	CriticalFindings int               `json:"critical_findings"`
	Blocking         bool              `json:"blocking"`
	StartedAt        string            `json:"started_at"`
	CompletedAt      string            `json:"completed_at"`
}

// CmdRunner abstracts OS command execution for testability.
type CmdRunner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// ExecCmdRunner is the production implementation using os/exec.
type ExecCmdRunner struct{}

// Run executes a command and returns combined stdout+stderr.
func (r *ExecCmdRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// EvaluatePrePush runs all pre-push validation phases for a feature branch.
// Phases: merged-state build+test+vet, security gate, static checks.
func EvaluatePrePush(ctx context.Context, runner CmdRunner, input PrePushInput) PrePushResult {
	startedAt := time.Now().UTC().Format(time.RFC3339)

	result := PrePushResult{
		Decision:         "allow",
		IssueNumber:      input.IssueNumber,
		TargetBranch:     input.TargetBranch,
		FeatureBranch:    input.FeatureBranch,
		ValidationPhases: make(map[string]string),
		StartedAt:        startedAt,
	}

	// Validate target branch name to prevent argument injection in git commands.
	if !validBranchName.MatchString(input.TargetBranch) {
		result.Decision = "block"
		result.Reason = fmt.Sprintf("Invalid target branch name %q: must match [a-zA-Z0-9\\-_./]", input.TargetBranch)
		result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		return result
	}

	workDir := input.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Failed to determine working directory: %v", err)
			result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			return result
		}
	}

	// Phase 1: Merged-state build+test+vet
	runMergedStateValidation(ctx, runner, workDir, input.TargetBranch, &result)

	// Phase 2: Security gate (only if not already blocked)
	if result.Decision != "block" {
		runPrePushSecurityGate(ctx, runner, workDir, input.TargetBranch, &result)
	}

	// Phase 3: Static checks (only if not already blocked)
	if result.Decision != "block" {
		runPrePushStaticChecks(ctx, runner, workDir, &result)
	}

	result.CompletedAt = time.Now().UTC().Format(time.RFC3339)

	// Write context file
	contextPath := writePrePushContextFile(workDir, input, result)
	result.ContextPath = contextPath

	return result
}

// runMergedStateValidation performs Phase 1: fetch target, merge, build, test, vet.
func runMergedStateValidation(ctx context.Context, runner CmdRunner, workDir, targetBranch string, result *PrePushResult) {
	result.ValidationPhases["merged_state"] = "passed"
	result.ValidationPhases["build"] = "skipped"
	result.ValidationPhases["test"] = "skipped"
	result.ValidationPhases["vet"] = "skipped"

	// Fetch latest target branch
	if _, err := runner.Run(ctx, workDir, "git", "fetch", "origin", targetBranch); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: git fetch origin %s failed: %v (continuing with local ref)\n", targetBranch, err)
	}

	// Save current branch
	branchOut, err := runner.Run(ctx, workDir, "git", "branch", "--show-current")
	if err != nil {
		result.ValidationPhases["merged_state"] = "failed"
		result.Decision = "block"
		result.Reason = "Failed to determine current branch"
		return
	}
	originalBranch := strings.TrimSpace(string(branchOut))

	// Create temp branch for merged-state testing
	tempBranch := fmt.Sprintf("temp-pre-push-%d", tempBranchNonce())
	if _, err := runner.Run(ctx, workDir, "git", "checkout", "-b", tempBranch); err != nil {
		result.ValidationPhases["merged_state"] = "failed"
		result.Decision = "block"
		result.Reason = "Failed to create temp branch for merged-state testing"
		return
	}

	// Always clean up: checkout back to original and delete temp branch. Never
	// reuse ctx here: it may have been cancelled by the timeout/interruption
	// that caused validation to return, which previously made every cleanup
	// command fail immediately and left the repository on temp-pre-push-* with
	// a merge in progress.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = runner.Run(cleanupCtx, workDir, "git", "merge", "--abort")
		if out, cleanupErr := runner.Run(cleanupCtx, workDir, "git", "checkout", originalBranch); cleanupErr != nil {
			result.ValidationPhases["merged_state"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Failed to restore original branch %s after validation: %s",
				originalBranch, truncateStr(strings.TrimSpace(string(out)), 500))
			return // never delete the currently checked-out temp branch
		}
		if out, cleanupErr := runner.Run(cleanupCtx, workDir, "git", "branch", "-D", tempBranch); cleanupErr != nil {
			result.ValidationPhases["merged_state"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Restored %s but failed to delete temporary branch %s: %s",
				originalBranch, tempBranch, truncateStr(strings.TrimSpace(string(out)), 500))
		}
	}()

	// Attempt merge with target
	mergeOutput, err := runner.Run(ctx, workDir, "git", "merge", "--no-commit", "--no-ff", fmt.Sprintf("origin/%s", targetBranch))
	if err != nil {
		result.ValidationPhases["merged_state"] = "failed"
		result.Decision = "block"
		result.Reason = fmt.Sprintf("Merge conflict with %s: %s", targetBranch, truncateStr(strings.TrimSpace(string(mergeOutput)), 500))
		return
	}

	// Detect project type and run build/test/vet against merged state
	if fileExists(filepath.Join(workDir, "go.mod")) {
		// Go project: build, test, vet
		if out, err := runner.Run(ctx, workDir, "go", "build", "./..."); err != nil {
			result.ValidationPhases["build"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Build failed against merged state: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["build"] = "passed"

		if out, err := runner.Run(ctx, workDir, "go", "test", "./..."); err != nil {
			result.ValidationPhases["test"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Tests failed against merged state: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["test"] = "passed"

		if out, err := runner.Run(ctx, workDir, "go", "vet", "./..."); err != nil {
			result.ValidationPhases["vet"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("go vet failed: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["vet"] = "passed"
	} else if fileExists(filepath.Join(workDir, "package.json")) {
		// Node.js project: build, test
		if out, err := runner.Run(ctx, workDir, "npm", "run", "build"); err != nil {
			result.ValidationPhases["build"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("npm build failed: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["build"] = "passed"

		if out, err := runner.Run(ctx, workDir, "npm", "test"); err != nil {
			result.ValidationPhases["test"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("npm test failed: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["test"] = "passed"
		result.ValidationPhases["vet"] = "skipped" // No vet for Node.js
	}
}

// runPrePushSecurityGate performs Phase 2: secret scanning via gitleaks + grep fallback.
func runPrePushSecurityGate(ctx context.Context, runner CmdRunner, workDir, targetBranch string, result *PrePushResult) {
	result.ValidationPhases["security"] = "passed"

	// Try gitleaks first
	gitleaksOutput, err := runner.Run(ctx, workDir, "gitleaks", "detect",
		"--source", ".",
		"--log-opts", fmt.Sprintf("origin/%s..HEAD", targetBranch),
		"--no-banner",
	)
	if err != nil {
		outputStr := string(gitleaksOutput)
		if strings.Contains(outputStr, "leaks found") || strings.Contains(outputStr, "RuleID:") {
			findings := strings.Count(outputStr, "RuleID:")
			if findings == 0 {
				findings = 1
			}
			result.CriticalFindings += findings
			result.ValidationPhases["security"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Security gate: %d critical finding(s) detected by gitleaks", findings)
			return
		}
		// gitleaks not installed or other error — fall through to grep fallback
	} else {
		return // gitleaks ran and passed
	}

	// Grep fallback: scan diff for common secret patterns
	diffOutput, err := runner.Run(ctx, workDir, "git", "diff", "--unified=0", fmt.Sprintf("origin/%s...HEAD", targetBranch))
	if err != nil {
		return // Can't diff — skip security check
	}

	diffStr := string(diffOutput)

	for _, re := range secretPatterns {
		if re.MatchString(diffStr) {
			result.CriticalFindings++
		}
	}

	if result.CriticalFindings > 0 {
		result.ValidationPhases["security"] = "failed"
		result.Decision = "block"
		result.Reason = fmt.Sprintf("Security gate: %d potential secret(s) detected in diff", result.CriticalFindings)
	}
}

// runPrePushStaticChecks performs Phase 3: generated file sync, JSON/YAML validation.
func runPrePushStaticChecks(ctx context.Context, runner CmdRunner, workDir string, result *PrePushResult) {
	result.ValidationPhases["static_checks"] = "passed"

	// Check IPC client generated file sync
	ipcFile := filepath.Join(workDir, "packages", "nightgauge-vscode", "src", "services", "IpcClient.generated.ts")
	if fileExists(ipcFile) {
		if out, err := runner.Run(ctx, workDir, "git", "diff", "--exit-code", ipcFile); err != nil {
			result.ValidationPhases["static_checks"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Generated file out of sync: IpcClient.generated.ts. Run 'make generate-ipc-client'.\n%s", truncateStr(string(out), 300))
			return
		}
	}
}

// writePrePushContextFile writes the pre-push-{N}.json context file.
func writePrePushContextFile(workDir string, input PrePushInput, result PrePushResult) string {
	contextDir := filepath.Join(workDir, ".nightgauge", "pipeline")
	_ = os.MkdirAll(contextDir, 0o755)

	contextFile := filepath.Join(contextDir, fmt.Sprintf("pre-push-%d.json", input.IssueNumber))

	overallStatus := "passed"
	blocking := false
	if result.Decision == "block" {
		overallStatus = "failed"
		blocking = true
	}

	ctxFile := PrePushContextFile{
		SchemaVersion:    "1.0",
		IssueNumber:      input.IssueNumber,
		TargetBranch:     input.TargetBranch,
		FeatureBranch:    input.FeatureBranch,
		OverallStatus:    overallStatus,
		ValidationPhases: result.ValidationPhases,
		CriticalFindings: result.CriticalFindings,
		Blocking:         blocking,
		StartedAt:        result.StartedAt,
		CompletedAt:      result.CompletedAt,
	}

	data, err := json.MarshalIndent(ctxFile, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to marshal pre-push context: %v\n", err)
		return ""
	}

	if err := os.WriteFile(contextFile, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write pre-push context: %v\n", err)
		return ""
	}

	return contextFile
}

// ReadPrePushContext reads and parses a pre-push-{N}.json context file.
// Returns nil if the file doesn't exist or can't be parsed.
func ReadPrePushContext(workDir string, issueNumber int) *PrePushContextFile {
	contextFile := filepath.Join(workDir, ".nightgauge", "pipeline", fmt.Sprintf("pre-push-%d.json", issueNumber))
	data, err := os.ReadFile(contextFile)
	if err != nil {
		return nil
	}

	var ctx PrePushContextFile
	if err := json.Unmarshal(data, &ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse pre-push context %s: %v\n", contextFile, err)
		return nil
	}
	return &ctx
}

var prePushIssuePattern = regexp.MustCompile(`^[^/]+/(\d+)`)

// validBranchName matches safe git branch names: alphanumeric, hyphens, underscores, dots, slashes.
// Rejects anything that could be interpreted as a flag (leading --) or contain shell metacharacters.
var validBranchName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-_./]*$`)

// tempBranchNonce generates a unique suffix for temporary branches.
// Overridable in tests for deterministic branch names.
var tempBranchNonce = func() int64 { return time.Now().UnixNano() }

// secretPatterns are compiled once at package level for secret scanning fallback.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)password\s*[:=]\s*['"][^'"]{8,}['"]`),
	regexp.MustCompile(`(?i)secret\s*[:=]\s*['"][^'"]{8,}['"]`),
	regexp.MustCompile(`AKIA[A-Z0-9]{16}`),
	regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*['"][^'"]{8,}['"]`),
	regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
}

// extractIssueFromBranch extracts the issue number from a branch name like feat/2609-description.
func extractIssueFromBranch(branch string) int {
	match := prePushIssuePattern.FindStringSubmatch(branch)
	if len(match) < 2 {
		return 0
	}
	n := 0
	for _, ch := range match[1] {
		n = n*10 + int(ch-'0')
	}
	return n
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
