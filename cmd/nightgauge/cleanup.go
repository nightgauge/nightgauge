package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/spf13/cobra"
)

// cleanupCmd is the operator escape hatch for leaked per-issue docker
// compose stacks. Tears down `issue-NNN` projects (containers, volumes,
// networks, locally-built images) that the pipeline left behind. See
// Issue #3050.
func cleanupCmd() *cobra.Command {
	var (
		orphaned     bool
		allFlag      bool
		dryRun       bool
		jsonOut      bool
		removeImages bool
	)
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Tear down leaked issue-NNN docker compose stacks",
		Long: `Operator escape hatch. Tears down docker compose stacks named issue-NNN
that the pipeline left behind (containers, volumes, networks, project-tagged
images).

By default targets only "orphaned" stacks — projects whose worktree directory
no longer exists. Use --all to tear down every issue-* compose project on the
host. The command is idempotent and safe to re-run.`,
		Example: `  nightgauge cleanup                # tear down orphaned stacks (default)
  nightgauge cleanup --all          # tear down every issue-* stack
  nightgauge cleanup --dry-run      # list what would be torn down
  nightgauge cleanup --json         # machine-readable output`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if !dockercompose.IsAvailable(ctx) {
				if jsonOut {
					return printJSON(map[string]interface{}{
						"available":   false,
						"projects":    []dockercompose.Project{},
						"results":     []dockercompose.TeardownResult{},
						"skipped":     true,
						"skip_reason": "docker not available",
					})
				}
				fmt.Println("docker not available — nothing to clean up")
				return nil
			}

			projects, err := dockercompose.ListIssueProjects(ctx)
			if err != nil {
				return fmt.Errorf("list compose projects: %w", err)
			}

			active, _ := listActiveWorktreeIssues()

			targets := selectCleanupTargets(projects, active, orphaned, allFlag)

			results := make([]dockercompose.TeardownResult, 0, len(targets))
			for _, p := range targets {
				res, err := dockercompose.TeardownProject(ctx, p.Name, dockercompose.TeardownOptions{
					DryRun:       dryRun,
					RemoveImages: removeImages,
				})
				if err != nil {
					// Soft-fail per project; record and continue.
					fmt.Fprintf(os.Stderr, "[WARN] cleanup: %s: %v\n", p.Name, err)
					continue
				}
				results = append(results, res)
			}

			if jsonOut {
				return printJSON(map[string]interface{}{
					"available": true,
					"projects":  projects,
					"results":   results,
					"dry_run":   dryRun,
					"all":       allFlag,
				})
			}

			if len(results) == 0 {
				fmt.Println("No matching issue-* compose projects to clean up.")
				return nil
			}
			for _, r := range results {
				prefix := "removed"
				if r.DryRun {
					prefix = "would remove"
				} else if r.Skipped {
					prefix = "skipped"
				}
				fmt.Printf("  %-12s %s", prefix, r.Project)
				if len(r.ImagesRemoved) > 0 {
					fmt.Printf(" (images: %s)", strings.Join(r.ImagesRemoved, ", "))
				}
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&orphaned, "orphaned", true, "Only tear down stacks whose worktree no longer exists (default)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Tear down every issue-* compose project")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be torn down without acting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	cmd.Flags().BoolVar(&removeImages, "remove-images", true, "Remove project-tagged images after tearing the stack down")
	return cmd
}

// selectCleanupTargets picks which compose projects to act on. When --all
// is set, every issue-* project is selected; otherwise only those whose
// worktree directory no longer appears in `git worktree list`.
func selectCleanupTargets(projects []dockercompose.Project, activeIssues map[int]bool, orphanedOnly, allFlag bool) []dockercompose.Project {
	if allFlag {
		return projects
	}
	var out []dockercompose.Project
	for _, p := range projects {
		if !orphanedOnly {
			out = append(out, p)
			continue
		}
		if !activeIssues[p.IssueNumber] {
			out = append(out, p)
		}
	}
	return out
}

// listActiveWorktreeIssues returns the set of issue numbers currently
// represented by an active git worktree. Errors are non-fatal — when git
// isn't available we treat the active set as empty so all known projects
// look orphaned (the user can always escape with --all).
func listActiveWorktreeIssues() (map[int]bool, error) {
	out := map[int]bool{}
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	data, err := cmd.Output()
	if err != nil {
		return out, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		base := filepath.Base(path)
		// Match either "issue-NNN" (TS WorktreeManager) or
		// "<repo>-issue-NNN" (Go execution.Manager) directory shapes.
		num, ok := extractIssueNumber(base)
		if ok {
			out[num] = true
		}
	}
	return out, nil
}

// extractIssueNumber returns the trailing issue number from a worktree
// directory base name. Accepts "issue-NNN" and "<prefix>-issue-NNN".
func extractIssueNumber(base string) (int, bool) {
	idx := strings.LastIndex(base, "issue-")
	if idx < 0 {
		return 0, false
	}
	tail := base[idx+len("issue-"):]
	if tail == "" {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(tail, "%d", &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
