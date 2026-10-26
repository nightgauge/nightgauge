package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/workorder"
	"github.com/spf13/cobra"
)

// nextCmd ranks Ready, unblocked work by an ordered programs file (#1481).
//
// Cross-repo work order was computed by a script outside this repository even
// though internal/depgraph already builds the cross-repo blocker graph. This
// verb is that computation in the binary: the programs file supplies the
// order, the boards supply Ready, and the graph supplies "unblocked".
func nextCmd() *cobra.Command {
	var (
		programsPath string
		owner        string
		project      int
		repos        []string
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "next --programs <file>",
		Short: "Rank Ready, unblocked issues by an ordered programs file",
		Long: `Read an ordered programs file (each program selects issues by a label) and
list, per program in rank order, the issues that are Ready on their board and
unblocked, then the Ready issues that are blocked, then Ready, unblocked issues
no program selects.

An issue is blocked when an open issue on a board that was read blocks it
(GitHub's blockedBy relation or a dependency declared in its body), when it is
in a dependency cycle, when it carries an exclude label (default: blocked), or
when its label set was truncated so an exclusion cannot be ruled out. A
dependency on no board that was read — usually an issue in another
repository — is looked up on GitHub, as the autonomous scheduler does: CLOSED
satisfies it, OPEN blocks, and a state that cannot be confirmed blocks and is
listed as unresolved. A closed blocker that is a program member (a program:
label) blocks until it carries the post-merge validation marker, written by
` + "`nightgauge hook post-merge`" + ` when the merge commit verifies green; it is listed
as unvalidated.

The repos read are --repos if given, else every repo the programs file names,
else the sibling checkouts that carry .nightgauge/config.yaml. Each repo's
board is resolved the same way the scheduler resolves it.

Items are ordered by board Priority (P0 first, unset last), then repo, then
issue number. No LLM participates.`,
		Example: `  nightgauge next --programs programs.yaml
  nightgauge next --programs programs.yaml --owner acme --repos acme-api,acme-web --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			programs, err := workorder.Load(programsPath)
			if err != nil {
				return fmt.Errorf("programs file: %w", err)
			}

			scope, err := resolveWorkOrderScope(cmd, programs, owner, project, repos)
			if err != nil {
				return err
			}
			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}
			res, err := rankWorkOrder(cmd.Context(), client, programs, scope)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(res)
			}
			printWorkOrder(cmd.OutOrStdout(), res)
			return nil
		},
	}

	cmd.Flags().StringVar(&programsPath, "programs", "", "Ordered programs YAML file (required)")
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub org/owner (default: config.yaml)")
	cmd.Flags().IntVar(&project, "project", 0, "Project board for repos that declare none (default: config.yaml)")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Repos to read (default: the programs file's repos)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("programs")
	return cmd
}

func printWorkOrder(w io.Writer, res *workorder.Result) {
	for i, p := range res.Programs {
		title := p.ID
		if p.Title != "" {
			title += " — " + p.Title
		}
		fmt.Fprintf(w, "%d. %s  [%s]\n", i+1, title, p.Selector)
		if len(p.Ready) == 0 && len(p.Blocked) == 0 {
			fmt.Fprintln(w, "   (no Ready issues)")
		}
		for _, it := range p.Ready {
			fmt.Fprintf(w, "   %-3s %s  %s\n", orDash(it.Priority), it.Ref, it.Title)
		}
		for _, it := range p.Blocked {
			fmt.Fprintf(w, "   %-3s %s  %s  (blocked: %s)\n", orDash(it.Priority), it.Ref, it.Title, blockedReasons(it))
		}
	}
	if len(res.UnprogrammedReady) > 0 {
		fmt.Fprintln(w, "\nUnprogrammed Ready:")
		for _, it := range res.UnprogrammedReady {
			fmt.Fprintf(w, "   %-3s %s  %s\n", orDash(it.Priority), it.Ref, it.Title)
		}
	}
	fmt.Fprintf(w, "\n%d ready, %d blocked, %d unprogrammed ready\n",
		res.Totals.Ready, res.Totals.Blocked, res.Totals.UnprogrammedReady)
}

// workOrderScope is the set of repositories, and their boards, that `next`
// and `program status` read.
type workOrderScope struct {
	owner       string
	workdir     string
	repoConfigs []depgraph.RepoConfig
}

// resolveWorkOrderScope picks the repos to read — repos if given, else the
// programs file's, else the workspace's sibling checkouts — and resolves each
// repo's board the way the scheduler does.
func resolveWorkOrderScope(cmd *cobra.Command, programs *workorder.File, owner string, project int, repos []string) (workOrderScope, error) {
	workdir, _ := os.Getwd()
	cfg, cfgErr := config.Load(workdir)
	if cfgErr != nil {
		cfg = nil
	}
	if cfg != nil {
		if !cmd.Flags().Changed("owner") && cfg.Owner != "" {
			owner = cfg.Owner
		}
		if !cmd.Flags().Changed("project") && cfg.ProjectNumber != 0 {
			project = cfg.ProjectNumber
		}
	}
	if owner == "" {
		return workOrderScope{}, fmt.Errorf("--owner is required (or set in config.yaml)")
	}

	names := repos
	if len(names) == 0 {
		names = programs.Repos()
	}
	ot := getOwnerType(cmd)
	var repoConfigs []depgraph.RepoConfig
	if len(names) > 0 {
		for _, r := range names {
			if i := strings.LastIndex(r, "/"); i >= 0 {
				r = r[i+1:]
			}
			repoConfigs = append(repoConfigs, schedulerRepoConfig(cfg, ot, config.RepoProjectQuery{
				Owner:       owner,
				Repo:        r,
				SharedBoard: project,
				StartDir:    workdir,
			}))
		}
	} else {
		repoConfigs = autoDetectRepos(cfg, owner, project)
	}
	if len(repoConfigs) == 0 {
		return workOrderScope{}, fmt.Errorf("no repos to read; use --repos or name repos in the programs file")
	}
	for _, rc := range repoConfigs {
		if rc.Project == 0 {
			return workOrderScope{}, fmt.Errorf("no project board resolved for %s; pass --project or set it in config.yaml", rc.FullName())
		}
	}
	return workOrderScope{owner: owner, workdir: workdir, repoConfigs: repoConfigs}, nil
}

// rankWorkOrder builds the cross-repo dependency graph over the scope's
// boards, resolves every dependency on no board read through the scheduler's
// own batch resolver (one request per repository), and ranks the result, so
// the program frontier fails closed exactly where dispatch does (#2573).
func rankWorkOrder(ctx context.Context, client *gh.Client, programs *workorder.File, scope workOrderScope) (*workorder.Result, error) {
	graph, err := depgraph.BuildGraph(ctx, client, scope.repoConfigs,
		workspaceRepoAliases(scope.workdir, scope.repoConfigs))
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	issueSvc := gh.NewIssueService(client)
	resolved := orchestrator.ResolveIssueStatesByKey(ctx, issueSvc, workorder.ExternalDependencies(graph))
	// (#2574) A closed program blocker releases only with the validation
	// marker, read through the trust gate as dispatch reads it.
	known, unknown := workorder.ValidationCandidates(graph, resolved)
	validation := orchestrator.ResolveValidationByKey(ctx, issueSvc, markerTrustGate(client, scope.workdir), known, unknown)
	return workorder.Rank(programs, graph, resolved, validation), nil
}

// blockedReasons names everything holding an item: its open blockers and
// labels, then each dependency whose state could not be resolved.
func blockedReasons(it workorder.Item) string {
	reasons := append([]string{}, it.BlockedBy...)
	for _, u := range it.Unvalidated {
		reasons = append(reasons, u+" (closed without validation marker)")
	}
	for _, u := range it.Unresolved {
		reasons = append(reasons, u+" (state unresolved)")
	}
	return strings.Join(reasons, ", ")
}
