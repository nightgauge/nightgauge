package main

import (
	"context"
	"fmt"
	"os"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// repoCmd returns the "repo" subcommand group for repository operations.
func repoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Repository settings operations",
	}
	cmd.AddCommand(repoSettingsCmd(), repoDisableAutoMergeCmd(), repoCheckAutoMergeCmd(), repoEnableDeleteBranchCmd())
	return cmd
}

// repoSettingsCmd returns the "repo settings" subcommand.
func repoSettingsCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Fetch repository settings (including auto-merge status)",
		Example: `  nightgauge repo settings --owner nightgauge --repo myrepo
  nightgauge repo settings --owner nightgauge --repo myrepo --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewSettingsService(client)
			settings, err := svc.GetRepositorySettings(cmd.Context(), owner, repo)
			if err != nil {
				return fmt.Errorf("fetch repository settings: %w", err)
			}

			if outputJSON {
				return printJSON(settings)
			}

			fmt.Printf("Repository:             %s\n", settings.RepoFullName)
			fmt.Printf("Allow auto-merge:        %v\n", settings.AllowAutoMerge)
			fmt.Printf("Delete branch on merge:  %v\n", settings.DeleteBranchOnMerge)
			if settings.AllowAutoMerge {
				fmt.Println()
				fmt.Println("WARNING: auto-merge is enabled. The pipeline's pr-merge stage requires")
				fmt.Println("exclusive control over PR merging. Run:")
				fmt.Printf("  nightgauge repo disable-auto-merge --owner %s --repo %s\n", owner, repo)
			}
			if !settings.DeleteBranchOnMerge {
				fmt.Println()
				fmt.Println("NOTE: delete_branch_on_merge is off. Merged PR branches will linger. Run:")
				fmt.Printf("  nightgauge repo enable-delete-branch --owner %s --repo %s\n", owner, repo)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

// repoDisableAutoMergeCmd returns the "repo disable-auto-merge" subcommand.
func repoDisableAutoMergeCmd() *cobra.Command {
	var (
		owner   string
		repo    string
		force   bool
		outJSON bool
	)

	cmd := &cobra.Command{
		Use:   "disable-auto-merge",
		Short: "Disable auto-merge on repository to restore pipeline control",
		Long: `Disables the GitHub repository-level allow_auto_merge setting.

The Nightgauge pipeline requires exclusive control over PR merging via the
pr-merge stage. When allow_auto_merge is enabled, PRs merge automatically once
CI passes, bypassing the pipeline's watch/resolve loop and recovery mechanisms.

Use --force to skip the confirmation prompt (e.g., in CI or automated contexts).`,
		Example: `  nightgauge repo disable-auto-merge --owner nightgauge --repo myrepo
  nightgauge repo disable-auto-merge --owner nightgauge --repo myrepo --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				fmt.Printf("This will disable allow_auto_merge on %s/%s.\n", owner, repo)
				fmt.Print("Continue? [y/N]: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" && confirm != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewSettingsService(client)
			if err := svc.DisableAutoMerge(cmd.Context(), owner, repo); err != nil {
				return fmt.Errorf("disable auto-merge: %w", err)
			}

			if outJSON {
				return printJSON(map[string]interface{}{
					"disabled":         true,
					"repository":       fmt.Sprintf("%s/%s", owner, repo),
					"allow_auto_merge": false,
				})
			}

			fmt.Printf("✓ auto-merge disabled on %s/%s\n", owner, repo)
			fmt.Println("  The pipeline's pr-merge stage now has exclusive control over PR merging.")
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&outJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

// autoMergeSettingsFetcher is the minimal surface of the settings service
// required by the check-auto-merge gate. Tests inject a fake.
type autoMergeSettingsFetcher interface {
	GetRepositorySettings(ctx context.Context, owner, repo string) (*gh.RepositorySettings, error)
}

// checkAutoMergeSettings returns the fetcher used by repoCheckAutoMergeCmd.
// Tests override this to inject a fake that does not hit the network.
var checkAutoMergeSettings = func() (autoMergeSettingsFetcher, error) {
	client, err := clientFromConfig()
	if err != nil {
		return nil, err
	}
	return gh.NewSettingsService(client), nil
}

// checkAutoMergeResult is the JSON shape for `repo check-auto-merge --json`.
// `allowed` follows *-gate semantics: true means the gate ALLOWS dispatch
// (auto-merge is OFF — the desired pipeline state); false means BLOCKED.
type checkAutoMergeResult struct {
	Allowed        bool   `json:"allowed"`
	AllowAutoMerge bool   `json:"allow_auto_merge"`
	Repository     string `json:"repository"`
	Reason         string `json:"reason"`
}

// remediationAutoMerge is rendered to stderr when the gate blocks. Kept as a
// const so the SKILL never needs to inline the same copy.
const remediationAutoMerge = `Auto-merge bypasses the pipeline's pr-merge stage, preventing:
  - Detection of check failures and self-healing logic
  - Proper watch/wait loops that keep the UI synchronized
  - Recovery mechanisms that address transient CI failures

To fix: run 'nightgauge repo disable-auto-merge --owner %s --repo %s'
(or via VSCode: 'Nightgauge: Disable Repository Auto-Merge').
See docs/GIT_WORKFLOW.md#auto-merge-and-pipeline-control for details.`

// repoCheckAutoMergeCmd returns the "repo check-auto-merge" subcommand.
// Exit semantics mirror *-gate check verbs: nil on ALLOW (auto-merge off),
// fmt.Errorf on BLOCK (auto-merge on). See ADR-001 / ADR-003 in
// .nightgauge/knowledge/features/3074-*.
func repoCheckAutoMergeCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "check-auto-merge",
		Short: "Gate: fail when allow_auto_merge is enabled",
		Long: `Gate-style verb that exits non-zero when the target repository has
allow_auto_merge enabled. The Nightgauge pipeline's pr-merge stage requires
exclusive control over PR merging; auto-merge bypasses self-healing, watch
loops, and UI sync. This verb deliberately mirrors the existing 'repo settings'
semantics — it does not model branch protection rules.`,
		Example: `  nightgauge repo check-auto-merge --owner nightgauge --repo myrepo
  nightgauge repo check-auto-merge --owner nightgauge --repo myrepo --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := checkAutoMergeSettings()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}

			settings, err := svc.GetRepositorySettings(cmd.Context(), owner, repo)
			if err != nil {
				return fmt.Errorf("check auto-merge: %w", err)
			}

			repoFull := settings.RepoFullName
			if repoFull == "" {
				repoFull = fmt.Sprintf("%s/%s", owner, repo)
			}

			result := checkAutoMergeResult{
				Allowed:        !settings.AllowAutoMerge,
				AllowAutoMerge: settings.AllowAutoMerge,
				Repository:     repoFull,
			}
			if settings.AllowAutoMerge {
				result.Reason = "allow_auto_merge is enabled — pipeline pr-merge requires exclusive merge control"
			} else {
				result.Reason = "allow_auto_merge is disabled"
			}

			if outputJSON {
				if err := printJSON(result); err != nil {
					return err
				}
			} else if !settings.AllowAutoMerge {
				fmt.Printf("Auto-merge check: PASSED\n")
				fmt.Printf("Repository: %s — allow_auto_merge=false\n", repoFull)
			} else {
				fmt.Fprintf(os.Stderr, "Auto-merge check: BLOCKED\n")
				fmt.Fprintf(os.Stderr, "Repository: %s has auto-merge enabled.\n\n", repoFull)
				fmt.Fprintf(os.Stderr, remediationAutoMerge+"\n", owner, repo)
			}

			if settings.AllowAutoMerge {
				return fmt.Errorf("auto-merge enabled on %s — run 'nightgauge repo disable-auto-merge --owner %s --repo %s'",
					repoFull, owner, repo)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}

// repoEnableDeleteBranchCmd returns the "repo enable-delete-branch" subcommand.
// Onboarding sets delete_branch_on_merge=true so GitHub deletes a PR's head
// branch on merge — pairing with the pipeline's post-merge worktree +
// local-branch teardown (#3969) so merged branches don't accumulate. Enabling
// it is benign, so this runs without a confirmation prompt (automation-friendly
// for repo-init).
func repoEnableDeleteBranchCmd() *cobra.Command {
	var (
		owner   string
		repo    string
		outJSON bool
	)

	cmd := &cobra.Command{
		Use:   "enable-delete-branch",
		Short: "Enable delete_branch_on_merge so merged PR branches are removed",
		Long: `Enables the GitHub repository-level delete_branch_on_merge setting.

When a PR merges, GitHub deletes its head branch automatically. Combined with
the pipeline's post-merge teardown (worktree + local branch, #3969), this keeps
both remote and local merged branches from piling up. Intended to run at repo
onboarding (nightgauge repo-init).`,
		Example: `  nightgauge repo enable-delete-branch --owner nightgauge --repo myrepo
  nightgauge repo enable-delete-branch --owner nightgauge --repo myrepo --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := clientFromConfig()
			if err != nil {
				return err
			}

			svc := gh.NewSettingsService(client)
			if err := svc.EnableDeleteBranchOnMerge(cmd.Context(), owner, repo); err != nil {
				return fmt.Errorf("enable delete-branch-on-merge: %w", err)
			}

			if outJSON {
				return printJSON(map[string]interface{}{
					"enabled":                true,
					"repository":             fmt.Sprintf("%s/%s", owner, repo),
					"delete_branch_on_merge": true,
				})
			}

			fmt.Printf("✓ delete_branch_on_merge enabled on %s/%s\n", owner, repo)
			fmt.Println("  Merged PR head branches are now removed automatically.")
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub organization or user")
	cmd.Flags().StringVar(&repo, "repo", "", "Repository name")
	cmd.Flags().BoolVar(&outJSON, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("repo")

	return cmd
}
