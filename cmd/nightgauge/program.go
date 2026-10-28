package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/workorder"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
	"github.com/spf13/cobra"
)

// programCmd groups the program verbs (#2573).
func programCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "program",
		Short: "Read the state of the programs in an ordered programs file",
	}
	cmd.AddCommand(programStatusCmd())
	return cmd
}

// labeledIssueLister lists every issue carrying a label, OPEN and CLOSED.
// gh.IssueService satisfies it; tests pass a fake.
type labeledIssueLister interface {
	ListIssuesByLabel(ctx context.Context, owner, repo, label string) ([]pkgtypes.Issue, error)
}

// programStatusCmd reports each program's open / closed / validated /
// dispatchable counts from the forge, so a program's progress is one command,
// not a hand-assembled query (#2573).
func programStatusCmd() *cobra.Command {
	var (
		programsPath string
		id           string
		owner        string
		project      int
		repos        []string
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "status --programs <file>",
		Short: "Report each program's open, closed, validated and dispatchable issues",
		Long: `Read an ordered programs file and report, per program in rank order, how
many of its issues (every issue its selector label marks in its repos) are
open, closed and validated, and which are dispatchable now.

Dispatchable is the frontier ` + "`nightgauge next`" + ` computes: open, Ready on its
board, and unblocked under the autonomous scheduler's rule — a blocker in any
repository blocks while it is open or its state cannot be confirmed, and a
closed program blocker blocks until it carries the validation marker. Validated
is a closed issue whose trusted comments carry the post-merge validation marker,
which ` + "`nightgauge hook post-merge`" + ` writes when the merge commit verifies green;
an issue closed without it is closed but not validated.

Repos and boards are resolved as ` + "`nightgauge next`" + ` resolves them. No LLM
participates.`,
		Example: `  nightgauge program status --programs programs.yaml
  nightgauge program status --programs programs.yaml --id billing --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			programs, err := workorder.Load(programsPath)
			if err != nil {
				return fmt.Errorf("programs file: %w", err)
			}
			if id != "" {
				if err := keepProgram(programs, id); err != nil {
					return err
				}
			}

			scope, err := resolveWorkOrderScope(cmd, programs, owner, project, repos)
			if err != nil {
				return err
			}
			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create github client: %w", err)
			}
			ranked, err := rankWorkOrder(cmd.Context(), client, programs, scope)
			if err != nil {
				return err
			}
			var repoNames []string
			for _, rc := range scope.repoConfigs {
				repoNames = append(repoNames, rc.FullName())
			}
			issues, err := listProgramIssues(cmd.Context(), gh.NewIssueService(client), programs, repoNames)
			if err != nil {
				return err
			}

			marked, err := readValidationMarks(cmd.Context(), markerTrustGate(client, scope.workdir), gh.NewIssueService(client), issues)
			if err != nil {
				return err
			}

			status := workorder.Status(programs, issues, ranked, marked)
			if jsonOut {
				return printJSON(status)
			}
			printProgramStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}

	cmd.Flags().StringVar(&programsPath, "programs", "", "Ordered programs YAML file (required)")
	cmd.Flags().StringVar(&id, "id", "", "Report only this program")
	cmd.Flags().StringVar(&owner, "owner", "", "GitHub org/owner (default: config.yaml)")
	cmd.Flags().IntVar(&project, "project", 0, "Project board for repos that declare none (default: config.yaml)")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Repos to read (default: the programs file's repos)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("programs")
	return cmd
}

// keepProgram narrows the programs file to the program named id.
func keepProgram(f *workorder.File, id string) error {
	for _, p := range f.Programs {
		if p.ID == id {
			f.Programs = []workorder.Program{p}
			return nil
		}
	}
	return fmt.Errorf("program %q is not in the programs file", id)
}

// listProgramIssues lists, for each program, every issue its selector labels
// in each repository it covers. repos are "owner/name". A repository that
// cannot be read fails the report: a partial count would misstate progress.
func listProgramIssues(ctx context.Context, lister labeledIssueLister, f *workorder.File, repos []string) ([]pkgtypes.Issue, error) {
	var out []pkgtypes.Issue
	for _, p := range f.Ordered() {
		sel := f.Selector(p)
		for _, repo := range repos {
			if !p.Covers(repo) {
				continue
			}
			o, r, ok := strings.Cut(repo, "/")
			if !ok {
				return nil, fmt.Errorf("repository %q is not owner/name", repo)
			}
			issues, err := lister.ListIssuesByLabel(ctx, o, r, sel)
			if err != nil {
				return nil, fmt.Errorf("program %s: %w", p.ID, err)
			}
			out = append(out, issues...)
		}
	}
	return out, nil
}

// readValidationMarks reads each closed issue's trusted comments and latest
// closing merge and returns the keys (workorder.IssueKey) of those whose
// marker validates that merge (#2574, #2629: orchestrator.MarkerValidates,
// the rule dispatch applies). A comment or merge read that fails fails the report, as an unreadable
// repository does: a validated count missing an issue would misstate progress.
func readValidationMarks(ctx context.Context, comments orchestrator.TrustedCommentReader, merges orchestrator.ClosingMergeReader, issues []pkgtypes.Issue) (map[string]bool, error) {
	marked := map[string]bool{}
	seen := map[string]bool{}
	for _, is := range issues {
		key := workorder.IssueKey(is.Repo, is.Number)
		if seen[key] || !strings.EqualFold(is.State, "CLOSED") {
			continue
		}
		seen[key] = true
		o, r, ok := strings.Cut(is.Repo, "/")
		if !ok {
			return nil, fmt.Errorf("repository %q is not owner/name", is.Repo)
		}
		cs, err := comments.TrustedComments(ctx, o, r, is.Number)
		if err != nil {
			return nil, fmt.Errorf("validation marker of %s#%d: %w", is.Repo, is.Number, err)
		}
		latest, err := merges.LatestClosingMerge(ctx, o, r, is.Number)
		if err != nil {
			return nil, fmt.Errorf("closing merge of %s#%d: %w", is.Repo, is.Number, err)
		}
		if orchestrator.MarkerValidates(cs, latest) {
			marked[key] = true
		}
	}
	return marked, nil
}

func printProgramStatus(w io.Writer, status []workorder.ProgramStatus) {
	for i, s := range status {
		title := s.ID
		if s.Title != "" {
			title += " — " + s.Title
		}
		fmt.Fprintf(w, "%d. %s  [%s]\n", i+1, title, s.Selector)
		fmt.Fprintf(w, "   %d issues: %d open, %d closed, %d validated, %d dispatchable\n",
			s.Total, s.Open, s.Closed, s.Validated, s.Dispatchable)
		for _, ref := range s.DispatchableRefs {
			fmt.Fprintf(w, "   ready    %s\n", ref)
		}
		for _, it := range s.Blocked {
			fmt.Fprintf(w, "   blocked  %s  (%s)\n", it.Ref, blockedReasons(it))
		}
	}
}
