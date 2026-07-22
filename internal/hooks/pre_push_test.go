package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockCmdRunner records calls and returns predefined results.
type mockCmdRunner struct {
	calls   []mockCmdCall
	results map[string]mockCmdResult
}

type mockCmdCall struct {
	Dir  string
	Name string
	Args []string
}

type mockCmdResult struct {
	Output []byte
	Err    error
}

func newMockRunner() *mockCmdRunner {
	return &mockCmdRunner{
		results: make(map[string]mockCmdResult),
	}
}

func (m *mockCmdRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	call := mockCmdCall{Dir: dir, Name: name, Args: args}
	m.calls = append(m.calls, call)

	key := name + " " + strings.Join(args, " ")
	if result, ok := m.results[key]; ok {
		return result.Output, result.Err
	}
	return []byte{}, nil
}

func (m *mockCmdRunner) set(key string, output string, err error) {
	m.results[key] = mockCmdResult{Output: []byte(output), Err: err}
}

func init() {
	// Use deterministic nonce in tests so mock keys match.
	tempBranchNonce = func() int64 { return 0 }
}

// setupCleanBranchMock configures a mock runner for a clean merge scenario.
func setupCleanBranchMock() *mockCmdRunner {
	m := newMockRunner()
	m.set("git fetch origin main", "", nil)
	m.set("git branch --show-current", "feat/42-test-feature\n", nil)
	m.set("git checkout -b temp-pre-push-0", "", nil)
	m.set("git merge --no-commit --no-ff origin/main", "", nil)
	m.set("go build ./...", "", nil)
	m.set("go test ./...", "", nil)
	m.set("go vet ./...", "", nil)
	m.set("git merge --abort", "", nil)
	m.set("git checkout feat/42-test-feature", "", nil)
	m.set("git branch -D temp-pre-push-0", "", nil)
	// Security: gitleaks not available → grep fallback
	m.set("gitleaks detect --source . --log-opts origin/main..HEAD --no-banner", "", fmt.Errorf("not found"))
	m.set("git diff --unified=0 origin/main...HEAD", "no secrets here", nil)
	// Static checks: IPC file doesn't exist (no error from git diff --exit-code)
	m.set("git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts", "", nil)
	return m
}

func TestEvaluatePrePush_PassesCleanBranch(t *testing.T) {
	tmpDir := t.TempDir()

	// Create go.mod so it detects as Go project
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create pipeline directory
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := setupCleanBranchMock()
	input := PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test-feature",
	}

	result := EvaluatePrePush(context.Background(), runner, input)

	if result.Decision != "allow" {
		t.Errorf("expected decision=allow, got %q (reason: %s)", result.Decision, result.Reason)
	}
	if result.ValidationPhases["merged_state"] != "passed" {
		t.Errorf("expected merged_state=passed, got %q", result.ValidationPhases["merged_state"])
	}
	if result.ValidationPhases["build"] != "passed" {
		t.Errorf("expected build=passed, got %q", result.ValidationPhases["build"])
	}
	if result.ValidationPhases["test"] != "passed" {
		t.Errorf("expected test=passed, got %q", result.ValidationPhases["test"])
	}
	if result.ValidationPhases["vet"] != "passed" {
		t.Errorf("expected vet=passed, got %q", result.ValidationPhases["vet"])
	}
	if result.ValidationPhases["security"] != "passed" {
		t.Errorf("expected security=passed, got %q", result.ValidationPhases["security"])
	}
	if result.CriticalFindings != 0 {
		t.Errorf("expected 0 critical findings, got %d", result.CriticalFindings)
	}
}

func TestEvaluatePrePush_BlocksOnMergeConflict(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := newMockRunner()
	runner.set("git fetch origin main", "", nil)
	runner.set("git branch --show-current", "feat/42-test\n", nil)
	runner.set("git checkout -b temp-pre-push-0", "", nil)
	runner.set("git merge --no-commit --no-ff origin/main", "CONFLICT (content): Merge conflict in file.go", fmt.Errorf("exit 1"))
	runner.set("git merge --abort", "", nil)
	runner.set("git checkout feat/42-test", "", nil)
	runner.set("git branch -D temp-pre-push-0", "", nil)

	result := EvaluatePrePush(context.Background(), runner, PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test",
	})

	if result.Decision != "block" {
		t.Errorf("expected decision=block, got %q", result.Decision)
	}
	if result.ValidationPhases["merged_state"] != "failed" {
		t.Errorf("expected merged_state=failed, got %q", result.ValidationPhases["merged_state"])
	}
	if !strings.Contains(result.Reason, "Merge conflict") {
		t.Errorf("expected reason to mention merge conflict, got %q", result.Reason)
	}
}

func TestEvaluatePrePush_BlocksOnBuildFailure(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := newMockRunner()
	runner.set("git fetch origin main", "", nil)
	runner.set("git branch --show-current", "feat/42-test\n", nil)
	runner.set("git checkout -b temp-pre-push-0", "", nil)
	runner.set("git merge --no-commit --no-ff origin/main", "", nil)
	runner.set("go build ./...", "cannot find package \"missing\"", fmt.Errorf("exit 1"))
	runner.set("git merge --abort", "", nil)
	runner.set("git checkout feat/42-test", "", nil)
	runner.set("git branch -D temp-pre-push-0", "", nil)

	result := EvaluatePrePush(context.Background(), runner, PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test",
	})

	if result.Decision != "block" {
		t.Errorf("expected decision=block, got %q", result.Decision)
	}
	if result.ValidationPhases["build"] != "failed" {
		t.Errorf("expected build=failed, got %q", result.ValidationPhases["build"])
	}
	if !strings.Contains(result.Reason, "Build failed") {
		t.Errorf("expected reason to mention build failure, got %q", result.Reason)
	}
}

func TestEvaluatePrePush_BlocksOnSecurityCritical(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	runner := setupCleanBranchMock()
	// Override security grep to find a secret
	runner.set("git diff --unified=0 origin/main...HEAD", `+password = "super_secret_password_12345"`, nil)

	result := EvaluatePrePush(context.Background(), runner, PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test-feature",
	})

	if result.Decision != "block" {
		t.Errorf("expected decision=block, got %q", result.Decision)
	}
	if result.ValidationPhases["security"] != "failed" {
		t.Errorf("expected security=failed, got %q", result.ValidationPhases["security"])
	}
	if result.CriticalFindings == 0 {
		t.Error("expected critical findings > 0")
	}
}

func TestPrePushResult_WriteContextFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	input := PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test",
	}
	result := PrePushResult{
		Decision:    "allow",
		IssueNumber: 42,
		ValidationPhases: map[string]string{
			"merged_state":  "passed",
			"build":         "passed",
			"test":          "passed",
			"vet":           "passed",
			"security":      "passed",
			"static_checks": "passed",
		},
		StartedAt:   "2026-04-08T00:00:00Z",
		CompletedAt: "2026-04-08T00:01:30Z",
	}

	contextPath := writePrePushContextFile(tmpDir, input, result)
	if contextPath == "" {
		t.Fatal("expected non-empty context path")
	}

	expectedPath := filepath.Join(tmpDir, ".nightgauge", "pipeline", "pre-push-42.json")
	if contextPath != expectedPath {
		t.Errorf("expected context path %q, got %q", expectedPath, contextPath)
	}

	// Verify file exists and is valid JSON
	data, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("failed to read context file: %v", err)
	}

	var ctx PrePushContextFile
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatalf("failed to parse context file: %v", err)
	}

	if ctx.SchemaVersion != "1.0" {
		t.Errorf("expected schema_version=1.0, got %q", ctx.SchemaVersion)
	}
	if ctx.OverallStatus != "passed" {
		t.Errorf("expected overall_status=passed, got %q", ctx.OverallStatus)
	}
	if ctx.Blocking {
		t.Error("expected blocking=false")
	}
	if ctx.IssueNumber != 42 {
		t.Errorf("expected issue_number=42, got %d", ctx.IssueNumber)
	}
}

func TestReadPrePushContext_ReturnsNilWhenMissing(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := ReadPrePushContext(tmpDir, 9999)
	if ctx != nil {
		t.Error("expected nil for missing context file")
	}
}

func TestReadPrePushContext_ParsesValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	ctx := PrePushContextFile{
		SchemaVersion:    "1.0",
		IssueNumber:      42,
		TargetBranch:     "main",
		FeatureBranch:    "feat/42-test",
		OverallStatus:    "passed",
		ValidationPhases: map[string]string{"build": "passed", "test": "passed"},
	}
	data, _ := json.Marshal(ctx)
	if err := os.WriteFile(filepath.Join(dir, "pre-push-42.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	result := ReadPrePushContext(tmpDir, 42)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.OverallStatus != "passed" {
		t.Errorf("expected overall_status=passed, got %q", result.OverallStatus)
	}
	if result.IssueNumber != 42 {
		t.Errorf("expected issue_number=42, got %d", result.IssueNumber)
	}
}

func TestExtractIssueFromBranch(t *testing.T) {
	tests := []struct {
		branch   string
		expected int
	}{
		{"feat/2609-pre-push-validation", 2609},
		{"fix/42-bug-fix", 42},
		{"docs/100-update-readme", 100},
		{"main", 0},
		{"", 0},
		{"some-branch-no-issue", 0},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			result := extractIssueFromBranch(tt.branch)
			if result != tt.expected {
				t.Errorf("extractIssueFromBranch(%q) = %d, want %d", tt.branch, result, tt.expected)
			}
		})
	}
}

func TestEvaluatePrePush_SkipsSecurityOnBlock(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}

	// Merge conflict → should skip security and static checks
	runner := newMockRunner()
	runner.set("git fetch origin main", "", nil)
	runner.set("git branch --show-current", "feat/42-test\n", nil)
	runner.set("git checkout -b temp-pre-push-0", "", nil)
	runner.set("git merge --no-commit --no-ff origin/main", "CONFLICT", fmt.Errorf("exit 1"))
	runner.set("git merge --abort", "", nil)
	runner.set("git checkout feat/42-test", "", nil)
	runner.set("git branch -D temp-pre-push-0", "", nil)

	result := EvaluatePrePush(context.Background(), runner, PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test",
	})

	// Security and static_checks should not be present since they were skipped
	if _, ok := result.ValidationPhases["security"]; ok {
		t.Error("expected security phase to be absent when merge failed")
	}
	if _, ok := result.ValidationPhases["static_checks"]; ok {
		t.Error("expected static_checks phase to be absent when merge failed")
	}
}

func TestEvaluatePrePush_BlocksInvalidBranchName(t *testing.T) {
	tests := []struct {
		name   string
		branch string
	}{
		{"flag injection", "--upload-pack=evil"},
		{"empty", ""},
		{"space", "main branch"},
		{"semicolon", "main;rm -rf /"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EvaluatePrePush(context.Background(), newMockRunner(), PrePushInput{
				IssueNumber:   1,
				WorkDir:       t.TempDir(),
				TargetBranch:  tt.branch,
				FeatureBranch: "feat/1-test",
			})
			if result.Decision != "block" {
				t.Errorf("expected decision=block for branch %q, got %q", tt.branch, result.Decision)
			}
		})
	}
}
