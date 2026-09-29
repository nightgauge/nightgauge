package doctor

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// Workspace-hygiene checks (#2089), native ADR-025 checks: one finding per
// leaked object, each with its own fingerprint and a declared remedy. All are
// housekeeping except a wedged serve lease, which blocks `serve` and is a
// warning.

func init() {
	native := func(id, title, code string, run func(ctx context.Context, env *Env) ([]Finding, string)) {
		builtinChecks = append(builtinChecks, Check{
			ID: id, Title: title, Group: "hygiene", Code: code,
			Run: func(ctx context.Context, env *Env) []Finding {
				fs, detail := run(ctx, env)
				env.SetDetail(id, detail)
				return fs
			},
		})
	}
	// Per-issue compose stacks whose worktree no longer exists.
	native("compose_orphans", "Orphaned compose projects", "NGD016",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return composeOrphanFindings(findOrphanedComposeProjects(ctx, env.Cwd))
		})
	native("worktree_leaks", "Leaked worktrees", "NGD017",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return worktreeLeakFindings(env.Cwd, env.Now, mergedPRDoor(ctx, env.Client))
		})
	// A branch whose worktree is already gone (#912).
	native("stranded_branches", "Stranded branches", "NGD018",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return strandedBranchFindings(env.Cwd, mergedPRDoor(ctx, env.Client))
		})
	native("pipeline_stashes", "Pipeline stashes", "NGD019",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return pipelineStashFindings(env.Cwd, env.Now, mergedPRDoor(ctx, env.Client))
		})
	// Work from a killed stage preserved under a WIP ref (#1105).
	native("preserved_wip", "Preserved WIP refs", "NGD020",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return preservedWipFindings(env.Cwd, env.Now)
		})
	// A stage that is never killed leaks itself (#341).
	native("orphaned_processes", "Orphaned processes", "NGD021",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return orphanedProcessFindings(env.Cwd, env.Now)
		})
	// The scheduler lease (#1349): a wedged holder blocks every start here.
	native("serve_lease", "Serve lease", "NGD022",
		func(ctx context.Context, env *Env) ([]Finding, string) {
			return serveLeaseFindings(env.Cwd, env.Now)
		})
}

// composeOrphanFindings reports one finding per `issue-N` compose project
// whose worktree no longer exists, with a confirm `compose.cleanup` remedy.
// An undetermined worktree set is reported as unverifiable, never as clean:
// with no readable set every stack looks orphaned (#280, #323).
func composeOrphanFindings(orphans []dockercompose.Project, determined bool) ([]Finding, string) {
	const check, code = "compose_orphans", "NGD016"
	if !determined {
		return []Finding{unverifiableFinding(check, code, SeverityHousekeeping, "orphaned compose projects",
				"could not read the active worktree set across the workspace's repo roots — not inside a git repository or workspace, or `git worktree list` failed. Do NOT run `nightgauge cleanup` on this basis; it would tear down live runs' stacks")},
			"could not determine which issues have an active worktree"
	}
	if len(orphans) == 0 {
		return nil, "no orphaned issue-* compose projects"
	}
	out := make([]Finding, 0, len(orphans))
	for _, p := range orphans {
		out = append(out, newFinding(check, code, SeverityHousekeeping,
			fmt.Sprintf("orphaned docker compose project %s", p.Name),
			fmt.Sprintf("no worktree for issue #%d exists in any of the workspace's repo roots, so nothing owns this stack", p.IssueNumber),
			map[string]string{"project": p.Name, "issue": strconv.Itoa(p.IssueNumber), "status": p.Status},
			[]string{p.Name},
			Remedy{ID: "cleanup", Kind: RemedyConfirm, Verb: verbComposeCleanup, Verify: check,
				Summary: "Tear down the orphaned stack with `nightgauge cleanup`",
				Preview: fmt.Sprintf("docker compose down for project %s (issue #%d); refused if a worktree for the issue exists again", p.Name, p.IssueNumber)}))
	}
	return out, fmt.Sprintf("%d orphaned issue-* compose project(s)", len(orphans))
}

// mergedPRDoor builds the merged-PR second door from doctor's own client
// (#916). A nil client yields the closed door and the content test alone.
func mergedPRDoor(ctx context.Context, client *gh.Client) mergedPRDoorFactory {
	return func(repoRoot string) execution.MergedPRLookup {
		lookup := gh.NewMergedPRLookupForRoot(ctx, func() (*gh.Client, error) { return client, nil }, repoRoot)
		if lookup == nil {
			return nil
		}
		return lookup
	}
}
