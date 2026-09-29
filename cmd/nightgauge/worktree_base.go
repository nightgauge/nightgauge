package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/spf13/cobra"
)

// worktreeBaseResult is `nightgauge worktree base --json`: the one resolver's
// answer (config.ResolveWorktreeBase, ADR-024 § 9) for the extension, which
// obtains worktree locations from the binary rather than re-deriving them.
type worktreeBaseResult struct {
	// RepoRoot is the main checkout the base was resolved for.
	RepoRoot string `json:"repoRoot"`
	// Base is the directory pipeline worktrees of that clone are created in.
	Base string `json:"base"`
	// Path is <base>/<repo>-issue-<N>, present when --repo and --issue are given.
	Path string `json:"path,omitempty"`
}

func worktreeBaseCmd() *cobra.Command {
	var (
		workdir string
		repo    string
		issue   int
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "base",
		Short: "Print the directory pipeline worktrees are created in",
		Long: `Print the directory pipeline worktrees of this clone are created in: the
machine- or local-tier pipeline.worktree_base, or, unset, the machine-state
directory keyed per clone (STATE/worktrees/<repo-key>), outside the working tree.

With --repo and --issue, also print the worktree path <base>/<repo>-issue-<N>.
A relative value, a value in the committed .nightgauge/config.yaml, or one that
resolves inside the working tree is an error naming the file, the line and the fix.`,
		Example: `  nightgauge worktree base
  nightgauge worktree base --workdir ~/src/app --repo acme/app --issue 42 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A refusal here is a configuration answer, not a usage mistake.
			cmd.SilenceUsage = true
			dir := workdir
			if dir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				dir = cwd
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", dir, err)
			}
			root := config.MainCheckoutRoot(abs)
			if root == "" {
				return fmt.Errorf("%s is not inside a git checkout; pass --workdir", abs)
			}
			base, err := config.ResolveWorktreeBase(root)
			if err != nil {
				return err
			}
			res := worktreeBaseResult{RepoRoot: root, Base: base}
			if repo != "" || issue != 0 {
				if repo == "" || issue <= 0 {
					return fmt.Errorf("--repo and --issue are required together")
				}
				p, err := layout.WorktreePath(base, repo, issue)
				if err != nil {
					return err
				}
				res.Path = p
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			if res.Path != "" {
				fmt.Fprintln(out, res.Path)
				return nil
			}
			fmt.Fprintln(out, res.Base)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory inside the checkout to resolve for (default: the current directory)")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository (owner/name or name) whose worktree path to print")
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number whose worktree path to print")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON")
	return cmd
}
