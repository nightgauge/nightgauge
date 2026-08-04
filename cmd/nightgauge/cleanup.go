package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
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

			// The worktree set is consulted only when it actually decides
			// something: `--all` and `--orphaned=false` both mean "tear down
			// every stack", which is the operator stating a decision rather than
			// the code inferring one from disk. Only the orphan inference can be
			// wrong about a live run, so only it is guarded.
			var active map[int]bool
			if orphaned && !allFlag {
				cwd, _ := os.Getwd()
				var determined bool
				active, determined = execution.ActiveWorktreeIssues(config.WorkspaceRepoRoots(cwd))
				if !determined {
					// Pre-#323 this discarded the error (`active, _ :=`) and
					// treated the empty map as fact, so a git failure — or a run
					// living in a sibling repo — made every stack look orphaned,
					// and `down -v` destroyed the live run's named volumes. The
					// scheduler already refuses this (#296); the operator-facing
					// path must refuse it too, because `doctor` sends the
					// operator here.
					return fmt.Errorf("refusing to tear down %d compose project(s): could not read the active worktree set across the workspace's repo roots, so every stack would look orphaned — including live runs'. Re-run inside the workspace, or use --all to tear down everything deliberately", len(projects))
				}
			}

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
