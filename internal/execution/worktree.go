package execution

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/dockercompose"
)

// ensureWorktree creates a git worktree for isolated execution.
// Path: {workspaceRoot}/.nightgauge/worktrees/issue-{N}/
func (m *Manager) ensureWorktree(repo string, issueNumber int) (string, error) {
	worktreeDir := m.worktreePath(repo, issueNumber)

	// Check if worktree already exists
	if _, err := os.Stat(worktreeDir); err == nil {
		return worktreeDir, nil
	}

	// Determine the repo root for the worktree source
	repoRoot := m.repoRoot(repo)
	if _, err := os.Stat(repoRoot); err != nil {
		return "", fmt.Errorf("repo root not found: %s", repoRoot)
	}

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return "", fmt.Errorf("create worktree parent: %w", err)
	}

	// Resolve the main repo's current HEAD commit, then create the worktree
	// in detached-HEAD state at that commit.
	//
	// Why detached instead of checking out a named branch: git forbids two
	// worktrees claiming the same branch simultaneously. The old behaviour
	// here was `git worktree add <dir> <current-branch-name>` — which failed
	// any time the main repo was on a branch (e.g. a developer's feature
	// branch, or in our own dogfooded workflow, the branch that just
	// shipped and hadn't been switched off). A single such failure trips
	// the safety-rails circuit breaker after 3 retries and stops the
	// autonomous scheduler until the user manually resumes.
	//
	// Pipeline skills create a per-issue branch inside the worktree as a
	// later step (`feat/<N>-<slug>`), so the worktree doesn't need to hold
	// a branch ref at creation time.
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = repoRoot
	headOutput, err := headCmd.Output()
	if err != nil {
		return "", fmt.Errorf("get current HEAD commit: %w", err)
	}
	headSHA := strings.TrimSpace(string(headOutput))

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreeDir, headSHA)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", string(output), err)
	}

	// Copy .nightgauge config files from the parent repo into the worktree
	// so adapter detection reads the same config as the main checkout.
	copyWorktreeConfig(repoRoot, worktreeDir)

	// Build SDK CLI artifacts for CLI adapters (codex, copilot, lm-studio).
	adapter := readAdapterFromWorktree(worktreeDir)
	if shouldBuildSdkCli(adapter) {
		if err := buildSdkCliInWorktree(worktreeDir, repoRoot); err != nil {
			return "", err
		}
	}

	return worktreeDir, nil
}

// copyWorktreeConfig copies .nightgauge/config.yaml and config.local.yaml
// from the parent repo root into the worktree so adapter detection works.
func copyWorktreeConfig(repoRoot, worktreeDir string) {
	srcDir := filepath.Join(repoRoot, ".nightgauge")
	dstDir := filepath.Join(worktreeDir, ".nightgauge")

	_ = os.MkdirAll(dstDir, 0755)

	for _, name := range []string{"config.yaml", "config.local.yaml"} {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(dstDir, name)
		if data, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(dst, data, 0644)
		}
	}
}

// readAdapterFromWorktree reads ui.core.adapter from the worktree's config files.
// It tries config.local.yaml first (personal preference), then config.yaml.
// Returns "claude" if no adapter is configured.
func readAdapterFromWorktree(worktreeDir string) string {
	paths := []string{
		filepath.Join(worktreeDir, ".nightgauge", "config.local.yaml"),
		filepath.Join(worktreeDir, ".nightgauge", "config.yaml"),
	}
	for _, p := range paths {
		if adapter := readAdapterFromYaml(p); adapter != "" {
			return adapter
		}
	}
	return "claude"
}

// readAdapterFromYaml extracts ui.core.adapter from a YAML config file using
// simple line-by-line parsing (no external YAML library required).
func readAdapterFromYaml(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(content), "\n")
	inUI := false
	inCore := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "ui:" {
			inUI = true
			continue
		}
		if inUI && trimmed == "core:" {
			inCore = true
			continue
		}

		// Detect when we exit ui: or core: sections
		if len(trimmed) > 0 && !strings.HasPrefix(trimmed, "#") {
			if !strings.HasPrefix(line, " ") {
				inUI = false
				inCore = false
			} else if len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' {
				inCore = false
			}
		}

		if inCore && strings.HasPrefix(trimmed, "adapter:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"'`)
				if val != "" {
					return val
				}
			}
		}
	}
	return ""
}

// shouldBuildSdkCli returns true for CLI adapters that require SDK CLI artifacts.
func shouldBuildSdkCli(adapter string) bool {
	switch adapter {
	case "codex", "copilot", "lm-studio":
		return true
	default:
		return false
	}
}

// buildSdkCliInWorktree runs the SDK CLI build inside the worktree (or copies it from main repo).
func buildSdkCliInWorktree(worktreeDir string, repoRoot string) error {
	srcDir := filepath.Join(repoRoot, "packages", "nightgauge-sdk", "dist")
	destDir := filepath.Join(worktreeDir, "packages", "nightgauge-sdk", "dist")

	if _, err := os.Stat(srcDir); err == nil {
		copyCmd := exec.Command("cp", "-R", srcDir, destDir)
		if err := copyCmd.Run(); err == nil {
			return nil
		}
	}

	cmd := exec.Command("npm", "run", "-w", "@nightgauge/sdk", "build")
	cmd.Dir = worktreeDir
	// Deterministic Node for the worktree SDK build (#3863): npm/node here must
	// resolve from the host's nvm `default` alias, not the ambient PATH of a
	// non-interactive spawn. No-op when node is already on PATH or unresolvable.
	cmd.Env, _ = applyNodeResolution(os.Environ())

	// 2-minute timeout for the build
	timer := time.AfterFunc(120*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"SDK CLI build failed in worktree (%s): %w\nBuild output:\n%s\n"+
				"This adapter requires built SDK CLI artifacts. "+
				"Check package.json scripts and dependencies.",
			worktreeDir, err, string(output),
		)
	}
	return nil
}

// CleanupWorktree removes a worktree after execution completes.
//
// Order: docker compose teardown for the per-issue stack runs FIRST (soft-fail
// — never blocks worktree removal), then `git worktree remove`. This prevents
// stale containers / volumes / networks / images named `issue-NNN-*` from
// surviving across pipeline runs and squatting host ports. See Issue #3050.
func (m *Manager) CleanupWorktree(repo string, issueNumber int) error {
	worktreeDir := m.worktreePath(repo, issueNumber)
	repoRoot := m.repoRoot(repo)
	projectName := fmt.Sprintf("issue-%d", issueNumber)

	// Soft-fail: docker may not be installed (dev machines without docker)
	// or the daemon may be down. Either case must not block worktree
	// removal — log a one-line WARN and continue.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if dockercompose.IsAvailable(ctx) {
		if _, err := dockercompose.TeardownProject(ctx, projectName, dockercompose.TeardownOptions{
			RemoveImages: true,
		}); err != nil {
			log.Printf("[WARN] worktree teardown: docker compose teardown for %s failed: %v", projectName, err)
		}
	}

	// Remove worktree via git
	cmd := exec.Command("git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		// If git worktree remove fails, try manual cleanup
		if rmErr := os.RemoveAll(worktreeDir); rmErr != nil {
			return fmt.Errorf("git worktree remove: %s (manual cleanup also failed: %v)", string(output), rmErr)
		}
		// Prune worktree references
		pruneCmd := exec.Command("git", "worktree", "prune")
		pruneCmd.Dir = repoRoot
		_ = pruneCmd.Run()
	}

	return nil
}

func (m *Manager) worktreePath(repo string, issueNumber int) string {
	// Use repo name (without owner) as the directory prefix
	repoName := repo
	if idx := strings.LastIndex(repo, "/"); idx >= 0 {
		repoName = repo[idx+1:]
	}
	return filepath.Join(m.workspaceRoot, ".nightgauge", "worktrees",
		fmt.Sprintf("%s-issue-%d", repoName, issueNumber))
}

func (m *Manager) repoRoot(repo string) string {
	// Resolve the run's target-repo root via the configured resolver so a
	// worktree is sourced from — and stays consistent with the run state in —
	// the correct repo in a multi-repo workspace. Falls back to workspaceRoot
	// when no resolver is set or the repo is unregistered (#229).
	return m.RepoRoot(repo)
}

// CleanupBranch deletes a local branch and its remote tracking branch.
// Idempotent — ignores errors for branches that don't exist.
// Protected branches (main, master) are never deleted.
func (m *Manager) CleanupBranch(branchName string) error {
	if branchName == "" || branchName == "main" || branchName == "master" {
		return nil
	}
	repoRoot := m.workspaceRoot

	// Delete local branch
	delLocal := exec.Command("git", "branch", "-D", branchName)
	delLocal.Dir = repoRoot
	_ = delLocal.Run() // ignore error — branch may not exist locally

	// Delete remote branch
	delRemote := exec.Command("git", "push", "origin", "--delete", branchName)
	delRemote.Dir = repoRoot
	_ = delRemote.Run() // ignore error — branch may not exist on remote

	// Prune stale remote-tracking refs
	prune := exec.Command("git", "remote", "prune", "origin")
	prune.Dir = repoRoot
	_ = prune.Run()

	return nil
}

// CleanupMergedBranches removes local branches whose remote tracking branch
// no longer exists (i.e., was deleted after PR merge). Protects main/master
// and the currently checked-out branch. Returns the list of deleted branch names.
func (m *Manager) CleanupMergedBranches() ([]string, error) {
	repoRoot := m.workspaceRoot

	// Prune stale remote-tracking refs first
	prune := exec.Command("git", "fetch", "--prune")
	prune.Dir = repoRoot
	_ = prune.Run()

	// Get current branch to protect it
	currentCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	currentCmd.Dir = repoRoot
	currentOut, err := currentCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("get current branch: %w", err)
	}
	currentBranch := strings.TrimSpace(string(currentOut))

	// List local branches with their tracking status
	// Format: <branchname> <upstream:track> — "gone" means remote was deleted
	listCmd := exec.Command("git", "for-each-ref", "--format=%(refname:short) %(upstream:track)", "refs/heads/")
	listCmd.Dir = repoRoot
	listOut, err := listCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	var deleted []string
	for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		branch := parts[0]
		track := ""
		if len(parts) > 1 {
			track = parts[1]
		}

		// Protect main, master, and current branch
		if branch == "main" || branch == "master" || branch == currentBranch {
			continue
		}

		// Delete branches whose remote tracking branch is gone
		if track == "[gone]" {
			delCmd := exec.Command("git", "branch", "-D", branch)
			delCmd.Dir = repoRoot
			if err := delCmd.Run(); err == nil {
				deleted = append(deleted, branch)
			}
		}
	}

	return deleted, nil
}
