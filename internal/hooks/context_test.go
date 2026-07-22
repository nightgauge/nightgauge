package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetBranch(t *testing.T) {
	dir := t.TempDir()
	setupFakeBranch(t, dir, "feat/42-test-feature")

	branch := getBranch(dir)
	if branch != "feat/42-test-feature" {
		t.Errorf("getBranch = %q, want feat/42-test-feature", branch)
	}
}

func TestGetBranchDetached(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("abc1234def5678\n"), 0644)

	branch := getBranch(dir)
	if branch != "" {
		t.Errorf("getBranch for detached HEAD = %q, want empty", branch)
	}
}

func TestGetBranchNoGit(t *testing.T) {
	dir := t.TempDir()
	branch := getBranch(dir)
	if branch != "" {
		t.Errorf("getBranch with no .git = %q, want empty", branch)
	}
}

func TestBuildContextMessage(t *testing.T) {
	ctx := ContextResult{
		Branch:           "feat/42-test",
		IssueNumber:      "42",
		LastCommit:       "feat: add feature",
		UncommittedCount: 3,
		PlanProgress:     "5/8 tasks (63%)",
	}

	msg := buildContextMessage(ctx)
	if msg == "" {
		t.Error("expected non-empty message")
	}

	// Verify key parts are present
	checks := []string{"Branch: feat/42-test", "Issue: #42", "Last commit:", "Uncommitted changes: 3", "Plan progress: 5/8"}
	for _, check := range checks {
		if !contains(msg, check) {
			t.Errorf("message missing %q:\n%s", check, msg)
		}
	}
}

func TestBuildContextMessageEmpty(t *testing.T) {
	msg := buildContextMessage(ContextResult{})
	if msg != "No context available" {
		t.Errorf("empty context message = %q, want 'No context available'", msg)
	}
}

func TestEvaluateContextJSON(t *testing.T) {
	dir := t.TempDir()
	setupFakeBranch(t, dir, "fix/99-bug-fix")

	data, err := EvaluateContextJSON(dir)
	if err != nil {
		t.Fatalf("EvaluateContextJSON: %v", err)
	}

	var result ContextResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if result.Branch != "fix/99-bug-fix" {
		t.Errorf("Branch = %q, want fix/99-bug-fix", result.Branch)
	}
	if result.IssueNumber != "99" {
		t.Errorf("IssueNumber = %q, want 99", result.IssueNumber)
	}
}

func TestGetPlanProgress(t *testing.T) {
	dir := t.TempDir()

	// Create a PLAN.md with some checkboxes
	plan := `# Plan
- [x] Task 1
- [x] Task 2
- [ ] Task 3
- [ ] Task 4
`
	os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte(plan), 0644)

	progress := getPlanProgress(dir)
	if progress != "2/4 tasks (50%)" {
		t.Errorf("getPlanProgress = %q, want '2/4 tasks (50%%)'", progress)
	}
}

func TestGetPlanProgressNoPlan(t *testing.T) {
	dir := t.TempDir()
	progress := getPlanProgress(dir)
	if progress != "" {
		t.Errorf("getPlanProgress with no plan = %q, want empty", progress)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsSubstr(s, substr)
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
