package forgecmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// repoCmd is the `forge repo` group — read-only repository metadata.
// Mirrors the gh-side surface used by repo-init / smart-setup /
// project-sync to discover the active repo's nameWithOwner / owner /
// name fields.
func repoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Repository metadata",
		Long:  longRepo,
	}
	cmd.AddCommand(repoViewCmd())
	return cmd
}

func repoViewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "view",
		Short:        "View repository metadata (nameWithOwner / owner / name)",
		Long:         longRepoView,
		SilenceUsage: true,
		Example:      `  nightgauge forge repo view --repo nightgauge/nightgauge --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			r, err := client.Repo().RepoMetadata(cmd.Context(), owner, repo)
			if err != nil {
				return emitError(cmd, fmt.Errorf("repo view: %w", err))
			}
			return renderForCmd(cmd, RepoFromForge(r))
		},
	}
	return cmd
}
