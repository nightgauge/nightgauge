package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// PrePushInput holds parameters for the pre-push validation gate.
type PrePushInput struct {
	IssueNumber   int
	WorkDir       string
	TargetBranch  string
	FeatureBranch string
	// TimeoutSeconds is the gate's limit: it bounds `go test -timeout` and is
	// named in a timeout reason. 0 means no limit.
	TimeoutSeconds int
}

// FailureKindTimeout marks a block caused by a limit expiring, not by a check
// failing (#2700).
const FailureKindTimeout = "timeout"

// runnerWaitDelay bounds how long Run waits for a killed command's children to
// release its output pipe. `go test`'s test binaries outlive a killed `go`, and
// without this CombinedOutput blocks past the deadline. A var for tests.
var runnerWaitDelay = 10 * time.Second

// PrePushResult holds the outcome of pre-push validation.
type PrePushResult struct {
	Decision      string `json:"decision"` // "allow" or "block"
	IssueNumber   int    `json:"issue_number"`
	TargetBranch  string `json:"target_branch"`
	FeatureBranch string `json:"feature_branch"`
	// ValidationPhases maps a phase to its outcome:
	//   "passed"         the phase ran and succeeded
	//   "failed"         the phase ran and failed (blocks the push)
	//   "timeout"        a limit expired during the phase (blocks the push;
	//                    says nothing about the code)
	//   "skipped"        the phase was not reached, or was skipped for the
	//                    reason in TestSkipReason
	//   "not-applicable" the project has no such phase (e.g. a Node project
	//                    with no `build` script) — recorded, not silent (#1159)
	//   "unknown"        the gate could not determine whether the phase
	//                    applies (blocks: a check that cannot run is not a pass)
	ValidationPhases map[string]string `json:"validation_phases"`
	CriticalFindings int               `json:"critical_findings"`
	Reason           string            `json:"reason,omitempty"`
	FailureKind      string            `json:"failure_kind,omitempty"`
	TestSkipReason   string            `json:"test_skip_reason,omitempty"`
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
	OverallStatus    string            `json:"overall_status"` // "passed", "failed" or "timeout"
	ValidationPhases map[string]string `json:"validation_phases"`
	CriticalFindings int               `json:"critical_findings"`
	Blocking         bool              `json:"blocking"`
	Reason           string            `json:"reason,omitempty"`
	FailureKind      string            `json:"failure_kind,omitempty"`
	TestSkipReason   string            `json:"test_skip_reason,omitempty"`
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
	cmd.WaitDelay = runnerWaitDelay
	out, err := cmd.CombinedOutput()
	// The command itself succeeded; only a child kept the pipe open.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return out, err
}

// gateTimedOut reports whether a failing step stopped because a limit expired:
// the gate's deadline, or Go's per-package timer.
func gateTimedOut(ctx context.Context, output []byte) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || isGoTestTimeout(output)
}

func isGoTestTimeout(output []byte) bool {
	return strings.Contains(string(output), "panic: test timed out after")
}

// blockIfTimedOut records a block as a timeout when the step that just failed
// stopped on a limit. It must run before a step's own failure reason, or a
// deadline reads as a merge conflict or a test failure.
func blockIfTimedOut(ctx context.Context, phase string, output []byte, timeoutSeconds int, result *PrePushResult) bool {
	if !gateTimedOut(ctx, output) {
		return false
	}
	result.ValidationPhases[phase] = "timeout"
	result.Decision = "block"
	result.FailureKind = FailureKindTimeout
	if ctx.Err() == nil {
		result.Reason = fmt.Sprintf("Pre-push gate timed out in the %s phase: Go's per-package test limit fired, "+
			"which can be a real hang (no assertion failed). Raise hooks.pre_push.timeout_seconds in "+
			".nightgauge/config.yaml or pass --timeout if the suite needs longer.", phase)
		return true
	}
	result.Reason = fmt.Sprintf("Pre-push gate timed out in the %s phase: the %ds limit expired (no test failed). "+
		"Raise hooks.pre_push.timeout_seconds in .nightgauge/config.yaml or pass --timeout.", phase, timeoutSeconds)
	return true
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
	runMergedStateValidation(ctx, runner, workDir, input.TargetBranch, input.TimeoutSeconds, &result)

	// Phase 2: Security gate (only if not already blocked)
	if result.Decision != "block" {
		runPrePushSecurityGate(ctx, runner, workDir, input.TargetBranch, &result)
		// The scan's fallback returns quietly when its diff fails, so an expired
		// gate would otherwise pass unscanned.
		if result.Decision != "block" && ctx.Err() != nil {
			blockIfTimedOut(ctx, "security", nil, input.TimeoutSeconds, &result)
		}
	}

	// Phase 3: Static checks (only if not already blocked)
	if result.Decision != "block" {
		runPrePushStaticChecks(ctx, runner, workDir, input.TimeoutSeconds, &result)
	}

	result.CompletedAt = time.Now().UTC().Format(time.RFC3339)

	// Write context file
	contextPath := writePrePushContextFile(workDir, input, result)
	result.ContextPath = contextPath

	return result
}

// runMergedStateValidation performs Phase 1: fetch target, merge, build, test, vet.
func runMergedStateValidation(ctx context.Context, runner CmdRunner, workDir, targetBranch string, timeoutSeconds int, result *PrePushResult) {
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
		if blockIfTimedOut(ctx, "merged_state", nil, timeoutSeconds, result) {
			return
		}
		result.ValidationPhases["merged_state"] = "failed"
		result.Decision = "block"
		result.Reason = "Failed to determine current branch"
		return
	}
	originalBranch := strings.TrimSpace(string(branchOut))

	// Create temp branch for merged-state testing
	tempBranch := fmt.Sprintf("temp-pre-push-%d", tempBranchNonce())
	if _, err := runner.Run(ctx, workDir, "git", "checkout", "-b", tempBranch); err != nil {
		if blockIfTimedOut(ctx, "merged_state", nil, timeoutSeconds, result) {
			return
		}
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
			result.FailureKind = ""
			result.Reason = fmt.Sprintf("Failed to restore original branch %s after validation: %s",
				originalBranch, truncateStr(strings.TrimSpace(string(out)), 500))
			return // never delete the currently checked-out temp branch
		}
		if out, cleanupErr := runner.Run(cleanupCtx, workDir, "git", "branch", "-D", tempBranch); cleanupErr != nil {
			result.ValidationPhases["merged_state"] = "failed"
			result.Decision = "block"
			result.FailureKind = ""
			result.Reason = fmt.Sprintf("Restored %s but failed to delete temporary branch %s: %s",
				originalBranch, tempBranch, truncateStr(strings.TrimSpace(string(out)), 500))
		}
	}()

	// The changed set is the branch's own work, so read it before the merge.
	changes := collectChangedPaths(ctx, runner, workDir, targetBranch)

	// Attempt merge with target
	mergeOutput, err := runner.Run(ctx, workDir, "git", "merge", "--no-commit", "--no-ff", fmt.Sprintf("origin/%s", targetBranch))
	if err != nil {
		if blockIfTimedOut(ctx, "merged_state", mergeOutput, timeoutSeconds, result) {
			return
		}
		result.ValidationPhases["merged_state"] = "failed"
		result.Decision = "block"
		result.Reason = fmt.Sprintf("Merge conflict with %s: %s", targetBranch, truncateStr(strings.TrimSpace(string(mergeOutput)), 500))
		return
	}

	// Detect project type and run build/test/vet against merged state
	if fileExists(filepath.Join(workDir, "go.mod")) {
		// Go project: build, test, vet
		if out, err := runner.Run(ctx, workDir, "go", "build", "./..."); err != nil {
			if blockIfTimedOut(ctx, "build", out, timeoutSeconds, result) {
				return
			}
			result.ValidationPhases["build"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Build failed against merged state: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["build"] = "passed"

		if skip, reason := decideGoTestSkip(ctx, runner, workDir, changes); skip {
			result.TestSkipReason = reason
		} else {
			fmt.Fprintf(os.Stderr, "Running go test: %s\n", reason)
			if out, err := runner.Run(ctx, workDir, "go", "test", "-timeout", goTestTimeoutArg(ctx, timeoutSeconds), "./..."); err != nil {
				if blockIfTimedOut(ctx, "test", out, timeoutSeconds, result) {
					return
				}
				result.ValidationPhases["test"] = "failed"
				result.Decision = "block"
				result.Reason = fmt.Sprintf("Tests failed against merged state: %s", truncateStr(string(out), 500))
				return
			}
			result.ValidationPhases["test"] = "passed"
		}

		if out, err := runner.Run(ctx, workDir, "go", "vet", "./..."); err != nil {
			if blockIfTimedOut(ctx, "vet", out, timeoutSeconds, result) {
				return
			}
			result.ValidationPhases["vet"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("go vet failed: %s", truncateStr(string(out), 500))
			return
		}
		result.ValidationPhases["vet"] = "passed"
	} else if fileExists(filepath.Join(workDir, "package.json")) {
		// Node.js project: build, test.
		//
		// Ask package.json which scripts EXIST before running them (#1159).
		// `npm run build` exits 1 with `Missing script: "build"` when the
		// script is not defined, and that is not a build failure — it is the
		// gate being unable to run a phase that does not exist. Treating the
		// two the same blocked every build-less Node project at pre-push with
		// `Reason: npm build failed`, while feature-validate (which does detect
		// the absence) passed the same tree: two gates, opposite verdicts, and
		// the run halted with build-failed after ~12 minutes.
		//
		// A package.json that cannot be read or parsed is a third thing again:
		// the gate cannot determine what to run, so it blocks with a distinct
		// reason rather than guessing in either direction.
		scripts, err := nodeScripts(filepath.Join(workDir, "package.json"))
		if err != nil {
			result.ValidationPhases["build"] = "unknown"
			result.ValidationPhases["test"] = "unknown"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("package.json could not be read, so build/test coverage is undetermined: %s", truncateStr(err.Error(), 500))
			return
		}

		if _, ok := scripts["build"]; !ok {
			// Recorded, not silent: pre-push-<N>.json carries
			// "build": "not-applicable" so a missing build step is visible
			// rather than indistinguishable from a build that passed.
			result.ValidationPhases["build"] = "not-applicable"
		} else if out, err := runner.Run(ctx, workDir, "npm", "run", "build"); err != nil {
			if blockIfTimedOut(ctx, "build", out, timeoutSeconds, result) {
				return
			}
			result.ValidationPhases["build"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("npm build failed: %s", truncateStr(string(out), 500))
			return
		} else {
			result.ValidationPhases["build"] = "passed"
		}

		if _, ok := scripts["test"]; !ok {
			result.ValidationPhases["test"] = "not-applicable"
		} else if out, err := runner.Run(ctx, workDir, "npm", "test"); err != nil {
			if blockIfTimedOut(ctx, "test", out, timeoutSeconds, result) {
				return
			}
			result.ValidationPhases["test"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("npm test failed: %s", truncateStr(string(out), 500))
			return
		} else {
			result.ValidationPhases["test"] = "passed"
		}

		result.ValidationPhases["vet"] = "not-applicable" // No vet for Node.js
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
func runPrePushStaticChecks(ctx context.Context, runner CmdRunner, workDir string, timeoutSeconds int, result *PrePushResult) {
	result.ValidationPhases["static_checks"] = "passed"

	// Check IPC client generated file sync
	ipcFile := filepath.Join(workDir, "packages", "nightgauge", "src", "services", "IpcClient.generated.ts")
	if fileExists(ipcFile) {
		if out, err := runner.Run(ctx, workDir, "git", "diff", "--exit-code", ipcFile); err != nil {
			if blockIfTimedOut(ctx, "static_checks", nil, timeoutSeconds, result) {
				return
			}
			result.ValidationPhases["static_checks"] = "failed"
			result.Decision = "block"
			result.Reason = fmt.Sprintf("Generated file out of sync: IpcClient.generated.ts. Run 'make generate-ipc-client'.\n%s", truncateStr(string(out), 300))
			return
		}
	}
}

// writePrePushContextFile writes the pre-push-{N}.json context file.
func writePrePushContextFile(workDir string, input PrePushInput, result PrePushResult) string {
	contextDir, err := cloneDir(layout.PipelineStateDir, workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to resolve pre-push context directory: %v\n", err)
		return ""
	}
	_ = os.MkdirAll(contextDir, 0o755)

	contextFile := filepath.Join(contextDir, fmt.Sprintf("pre-push-%d.json", input.IssueNumber))

	overallStatus := "passed"
	blocking := false
	if result.Decision == "block" {
		overallStatus = "failed"
		blocking = true
		if result.FailureKind == FailureKindTimeout {
			overallStatus = "timeout"
		}
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
		Reason:           result.Reason,
		FailureKind:      result.FailureKind,
		TestSkipReason:   result.TestSkipReason,
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
	contextDir, err := cloneDir(layout.PipelineStateDir, workDir)
	if err != nil {
		return nil
	}
	contextFile := filepath.Join(contextDir, fmt.Sprintf("pre-push-%d.json", issueNumber))
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

// nodeScripts returns the `scripts` block of a package.json. A package.json
// that is absent, unreadable or not valid JSON is an error rather than an empty
// map: "this project defines no scripts" and "I could not find out what scripts
// this project defines" are different facts, and only the first one licenses
// skipping a phase (#1159).
func nodeScripts(packageJSONPath string) (map[string]string, error) {
	raw, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return nil, err
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", packageJSONPath, err)
	}
	if pkg.Scripts == nil {
		pkg.Scripts = map[string]string{}
	}
	return pkg.Scripts, nil
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
