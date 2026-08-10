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
// Path: {workspaceRoot}/.nightgauge/worktrees/{repo}-issue-{N}/ — derived by
// worktreePath, the same function CleanupWorktree tears down with. The
// "{repo}-" prefix is load-bearing: every run in the workspace shares one
// worktrees/ root, so two repos' issue #{N} would collide without it. The bare
// "issue-{N}" shape belongs to the VSCode extension's WorktreeManager; both are
// read back by IssueNumberFromWorktreeDir (#400). See
// docs/GO_BINARY.md#worktree-directory-name-shapes.
//
// Error contract (#399): on error the returned path is non-empty iff the
// worktree exists on disk. Provisioning does not end when `git worktree add`
// returns — the SDK-CLI build for CLI adapters runs after it and can fail — and
// a run whose worktree is already on disk must stay resolvable through that
// failure. Returning "" there is what left RunStage unable to stamp the
// runtime, so stageWorkspace fell back to the workspace root and the failure
// path inspected the wrong tree. A non-nil error always means failure,
// whatever the path: the path only ever names a tree that exists.
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
		// The add is the creation boundary: it normally leaves nothing behind,
		// but the error contract above is stated as "non-empty iff on disk", so
		// ask the disk rather than assume which side of the boundary we are on.
		if _, statErr := os.Stat(worktreeDir); statErr == nil {
			return worktreeDir, fmt.Errorf("git worktree add: %s: %w", string(output), err)
		}
		return "", fmt.Errorf("git worktree add: %s: %w", string(output), err)
	}

	// Copy .nightgauge config files from the parent repo into the worktree
	// so adapter detection reads the same config as the main checkout.
	copyWorktreeConfig(repoRoot, worktreeDir)

	// Build SDK CLI artifacts for CLI adapters (codex, copilot, lm-studio).
	adapter := readAdapterFromWorktree(worktreeDir)
	if shouldBuildSdkCli(adapter) {
		if err := buildSdkCliInWorktree(worktreeDir, repoRoot); err != nil {
			// The worktree is on disk and registered with git — the caller must
			// be able to name it even though provisioning failed (#399).
			return worktreeDir, err
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

	// Preserve a worktree with uncommitted tracked changes rather than
	// destroying work a developer may still need to inspect — a terminal
	// state (including failure) is called unconditionally, and this is the
	// only guard standing between that and data loss. Missing/removed
	// worktrees read as "not dirty" (nothing to lose) and fall through to
	// the idempotent removal below.
	//
	// The pipeline's own untracked exhaust does not count as work here, for
	// the same reason it does not in the sweep (#332): this teardown runs on
	// the happy path, so a scaffolded knowledge README preserving the worktree
	// is how the leak is CREATED, one finished run at a time. See
	// blockingChanges.
	if blocking, err := blockingChanges(worktreeDir); err == nil && len(blocking) > 0 {
		log.Printf("worktree teardown: preserving %s (issue #%d) — %s (%s)",
			worktreeDir, issueNumber, SkipDirty, strings.Join(blocking, ", "))
		return nil
	}

	// Preserve a worktree whose current HEAD carries commits the default
	// branch doesn't have — a clean tree does not mean "safe to delete" when a
	// killed stage committed valid work and never got to push it (#266). This
	// checks whatever branch the worktree actually has checked out, not only
	// the issue's expected feature branch, so it also catches the case where a
	// SIGKILL mid pre-push-validate left HEAD on a stray `temp-pre-push-<n>`
	// branch.
	if branchOut, err := gitOutput(worktreeDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch := strings.TrimSpace(branchOut)
		if ahead, aerr := branchAheadOfBase(worktreeDir, branch); aerr == nil && ahead {
			log.Printf("worktree teardown: preserving %s (issue #%d) — branch %s carries commits not on the default branch (%s)",
				worktreeDir, issueNumber, branch, SkipUnmergedContent)
			return nil
		}
	}

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
		// A failure here used to be silent whenever the manual fallback
		// succeeded, which is how leaked worktrees stayed invisible until
		// someone counted them days later (#110). Log it.
		log.Printf("[WARN] worktree teardown: git worktree remove %s failed (%v): %s — falling back to manual removal",
			worktreeDir, err, strings.TrimSpace(string(output)))
		// If git worktree remove fails, try manual cleanup
		if rmErr := os.RemoveAll(worktreeDir); rmErr != nil {
			return fmt.Errorf("git worktree remove: %s (manual cleanup also failed: %v)", string(output), rmErr)
		}
		// Prune worktree references
		pruneCmd := exec.Command("git", "worktree", "prune")
		pruneCmd.Dir = repoRoot
		if pruneOut, pruneErr := pruneCmd.CombinedOutput(); pruneErr != nil {
			log.Printf("[WARN] worktree teardown: git worktree prune after manual removal of %s failed (%v): %s",
				worktreeDir, pruneErr, strings.TrimSpace(string(pruneOut)))
		}
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

// CleanupLocalBranch deletes only the local ref and prunes stale
// remote-tracking refs. It exists so a caller that must NOT touch origin — a
// failed run whose remote branch is holding an open PR, or one whose remote
// head came from someone else (#163) — can still drop its local ref without
// reaching for CleanupBranch's unconditional remote delete.
//
// Guarded against deleting a branch that carries commits not yet on the
// default branch (#266): the pre-fix behavior deleted the local ref
// unconditionally, which is how a killed stage's validated-but-unpushed
// commit became unreachable the moment CleanupWorktree ran next. Uses repo to
// resolve the same repo root the content-diff check elsewhere in this file
// uses, independent of the branch's dirty state (a branch ref carries no
// working tree of its own).
//
// Idempotent; protected branches are never deleted.
func (m *Manager) CleanupLocalBranch(repo, branchName string) error {
	if branchName == "" || branchName == "main" || branchName == "master" {
		return nil
	}
	repoRoot := m.workspaceRoot

	if ahead, err := branchAheadOfBase(m.repoRoot(repo), branchName); err == nil && ahead {
		log.Printf("branch cleanup: preserving local branch %s — carries commits not on the default branch (%s)",
			branchName, SkipUnmergedContent)
		return nil
	}

	delLocal := exec.Command("git", "branch", "-D", branchName)
	delLocal.Dir = repoRoot
	_ = delLocal.Run() // ignore error — branch may not exist locally

	prune := exec.Command("git", "remote", "prune", "origin")
	prune.Dir = repoRoot
	_ = prune.Run()

	return nil
}

// CleanupBranch deletes a local branch and its remote tracking branch.
// Idempotent — ignores errors for branches that don't exist.
// Protected branches (main, master) are never deleted.
//
// The unconditional remote delete is correct only for a run that SHIPPED: the
// PR is merged, so origin's copy is spent. On a failed run use
// CleanupLocalBranch plus the guarded ReclaimOrphanedRemoteBranch, which drops
// origin's copy only when the pipeline itself pushed it.
func (m *Manager) CleanupBranch(repo, branchName string) error {
	if branchName == "" || branchName == "main" || branchName == "master" {
		return nil
	}
	repoRoot := m.workspaceRoot

	// Delete remote branch
	delRemote := exec.Command("git", "push", "origin", "--delete", branchName)
	delRemote.Dir = repoRoot
	_ = delRemote.Run() // ignore error — branch may not exist on remote

	// Delete local branch + prune stale remote-tracking refs
	return m.CleanupLocalBranch(repo, branchName)
}

// CleanupBranchIfMerged deletes branchName's local ref only when its content
// is already fully represented on the repo's default branch, using the same
// content-diff check the worktree reclamation sweep relies on (mergedIntoBase
// in worktree_sweep.go) rather than an ancestry check — this project squash-
// merges, so `git merge-base --is-ancestor` false-negatives on every merged
// branch (see docs/GO_BINARY.md#worktree-reclamation-issue-110).
//
// Soft-fail throughout: an unresolvable default branch, a failed content-diff
// check, an unmerged branch, or a branch with no commits of its own all just
// log and return nil without deleting — this must never fail the caller's run.
func (m *Manager) CleanupBranchIfMerged(repo, branchName string) error {
	if branchName == "" || branchName == "main" || branchName == "master" {
		return nil
	}
	if merged := m.branchMergedIntoDefault(repo, branchName); !merged {
		return nil
	}
	return m.CleanupLocalBranch(repo, branchName)
}

// branchMergedIntoDefault reports whether branchName's content is already
// fully represented on repo's default branch, logging (never erroring) the
// same skip vocabulary the worktree reclamation sweep uses. Shared by
// CleanupBranchIfMerged (local-only delete) and the scheduler's success-path
// remote+local delete — both must clear the same content-diff gate before
// touching a branch (#106).
func (m *Manager) branchMergedIntoDefault(repo, branchName string) bool {
	repoRoot := m.repoRoot(repo)

	defaultBranch := detectDefaultBranch(repoRoot)
	baseRef, err := resolveBaseRef(repoRoot, defaultBranch)
	if err != nil {
		log.Printf("[WARN] branch cleanup: resolve base ref for %s failed: %v — leaving %s in place", repoRoot, err, branchName)
		return false
	}

	merged, hasOwnCommits, err := mergedIntoBase(repoRoot, baseRef, branchName)
	if err != nil {
		log.Printf("[WARN] branch cleanup: content-diff check for %s against %s failed: %v — leaving branch in place", branchName, baseRef, err)
		return false
	}
	if !hasOwnCommits {
		log.Printf("branch cleanup: %s has no commits of its own — leaving in place (%s)", branchName, SkipNoOwnCommits)
		return false
	}
	if !merged {
		log.Printf("branch cleanup: %s carries unmerged content vs %s — leaving in place (%s)", branchName, baseRef, SkipUnmergedContent)
		return false
	}
	return true
}

// CleanupBranchAndRemoteIfMerged is CleanupBranch (local + remote delete)
// gated by the same content-diff merged check as CleanupBranchIfMerged. Used
// by the scheduler's shipped-run path (#106): pipelineSuccess already implies
// the PR merged, but this gate is the load-bearing safety net rather than
// trusting outcome classification alone before an irreversible `git branch -D`
// / `git push --delete`.
func (m *Manager) CleanupBranchAndRemoteIfMerged(repo, branchName string) error {
	if branchName == "" || branchName == "main" || branchName == "master" {
		return nil
	}
	if merged := m.branchMergedIntoDefault(repo, branchName); !merged {
		return nil
	}
	return m.CleanupBranch(repo, branchName)
}

// CleanupMergedBranches removes local branches whose remote tracking branch
// no longer exists (i.e., was deleted after PR merge). Protects main/master
// and the currently checked-out branch. Returns the list of deleted branch names.
func (m *Manager) CleanupMergedBranches() ([]string, error) {
	repoRoot := m.workspaceRoot

	// Prune stale remote-tracking refs first. This is NOT best-effort: the
	// "[gone]" marker this function keys on only appears once the stale
	// remote-tracking ref is pruned. If the fetch fails — no network, expired
	// credentials, a remote that no longer resolves — nothing is ever marked
	// gone, the loop below matches nothing, and this returns ([], nil): a
	// clean "0 branches cleaned" that is indistinguishable from "there was
	// nothing to clean" (#166). Report it instead of guessing.
	prune := exec.Command("git", "fetch", "--prune")
	prune.Dir = repoRoot
	if out, err := prune.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fetch --prune (required to detect merged branches): %w: %s",
			err, strings.TrimSpace(string(out)))
	}

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
			if out, err := delCmd.CombinedOutput(); err == nil {
				deleted = append(deleted, branch)
			} else {
				// A branch git refuses to delete — most often one checked out
				// in another worktree — is silently absent from the returned
				// list, which reads as "not a candidate" rather than "tried
				// and failed". Name it so a persistent failure is visible.
				log.Printf("[WARN] cleanup: git branch -D %s failed (%v): %s",
					branch, err, strings.TrimSpace(string(out)))
			}
		}
	}

	return deleted, nil
}
