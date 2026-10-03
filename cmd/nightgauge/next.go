package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
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
dependency on a repo outside the read set is listed as unresolved, not
treated as blocking.

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
				return fmt.Errorf("--owner is required (or set in config.yaml)")
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
				return fmt.Errorf("no repos to read; use --repos or name repos in the programs file")
			}
			for _, rc := range repoConfigs {
				if rc.Project == 0 {
					return fmt.Errorf("no project board resolved for %s; pass --project or set it in config.yaml", rc.FullName())
				}
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}
			graph, err := depgraph.BuildGraph(context.Background(), client, repoConfigs,
				workspaceRepoAliases(workdir, repoConfigs))
			if err != nil {
				return fmt.Errorf("build graph: %w", err)
			}

			res := workorder.Rank(programs, graph)
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
			fmt.Fprintf(w, "   %-3s %s  %s  (blocked: %s)\n", orDash(it.Priority), it.Ref, it.Title, strings.Join(it.BlockedBy, ", "))
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
